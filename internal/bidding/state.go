package bidding

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	//go:embed lua/place.lua
	placeSrc string
	//go:embed lua/revert.lua
	revertSrc string
	//go:embed lua/close.lua
	closeSrc string

	placeScript  = redis.NewScript(placeSrc)
	revertScript = redis.NewScript(revertSrc)
	closeScript  = redis.NewScript(closeSrc)
)

// snapshot is the live auction state kept in Redis (hash auction:{listing_id}).
type snapshot struct {
	Round  int32
	Seller uuid.UUID
	Start  int64
	Incr   int64
	Ends   time.Time
	Closed bool
	High   int64     // 0 = no bids yet
	Bidder uuid.UUID // uuid.Nil = no bids yet
	Token  string
}

// prior is what a bid replaced, kept so a failed write can be reverted.
type prior struct {
	High   string
	Bidder string
	Token  string
	Ends   string
}

type placeResult struct {
	Status      string
	MinRequired int64
	Prev        prior
	NewEnds     time.Time
	Extended    bool
}

// state wraps the Redis side of auctions.
type state struct {
	rdb redis.UniversalClient
}

func auctionKey(id uuid.UUID) string { return "auction:" + id.String() }

func (s *state) place(ctx context.Context, listingID, bidder uuid.UUID, amount int64, token string, round int32, window, extension time.Duration) (placeResult, error) {
	res, err := placeScript.Run(ctx, s.rdb, []string{auctionKey(listingID)},
		bidder.String(), amount, token, round, window.Milliseconds(), extension.Milliseconds()).StringSlice()
	if err != nil {
		return placeResult{}, fmt.Errorf("place bid script: %w", err)
	}
	out := placeResult{Status: res[0]}
	switch out.Status {
	case "too_low":
		out.MinRequired, _ = strconv.ParseInt(res[1], 10, 64)
	case "ok":
		out.Prev = prior{High: res[1], Bidder: res[2], Token: res[3], Ends: res[4]}
		newEnds, _ := strconv.ParseInt(res[5], 10, 64)
		out.NewEnds = time.UnixMilli(newEnds).UTC()
		out.Extended = res[6] == "1"
	}
	return out, nil
}

func (s *state) revert(ctx context.Context, listingID uuid.UUID, token string, p prior) error {
	return revertScript.Run(ctx, s.rdb, []string{auctionKey(listingID)}, token, p.High, p.Bidder, p.Token, p.Ends).Err()
}

// close returns "closed", "not_due" (with the current end), "missing" or "round_mismatch".
func (s *state) close(ctx context.Context, listingID uuid.UUID, round int32) (string, time.Time, error) {
	res, err := closeScript.Run(ctx, s.rdb, []string{auctionKey(listingID)}, round).StringSlice()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("close script: %w", err)
	}
	var ends time.Time
	if res[0] == "not_due" && len(res) > 1 {
		ms, _ := strconv.ParseInt(res[1], 10, 64)
		ends = time.UnixMilli(ms).UTC()
	}
	return res[0], ends, nil
}

// load overwrites Redis with state rebuilt from Postgres (the source of truth).
func (s *state) load(ctx context.Context, listingID uuid.UUID, snap snapshot) error {
	key := auctionKey(listingID)
	bidder := ""
	if snap.Bidder != uuid.Nil {
		bidder = snap.Bidder.String()
	}
	closed := "0"
	if snap.Closed {
		closed = "1"
	}
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, key)
		p.HSet(ctx, key,
			"round", snap.Round, "seller", snap.Seller.String(), "start", snap.Start, "incr", snap.Incr,
			"ends", snap.Ends.UnixMilli(), "closed", closed, "high", snap.High, "bidder", bidder, "token", snap.Token)
		p.PExpireAt(ctx, key, snap.Ends.Add(24*time.Hour))
		return nil
	})
	return err
}

// live reads the current state; ok is false when Redis has nothing for the listing.
func (s *state) live(ctx context.Context, listingID uuid.UUID) (snapshot, bool, error) {
	m, err := s.rdb.HGetAll(ctx, auctionKey(listingID)).Result()
	if err != nil {
		return snapshot{}, false, err
	}
	if len(m) == 0 {
		return snapshot{}, false, nil
	}
	round, _ := strconv.ParseInt(m["round"], 10, 32)
	start, _ := strconv.ParseInt(m["start"], 10, 64)
	incr, _ := strconv.ParseInt(m["incr"], 10, 64)
	ends, _ := strconv.ParseInt(m["ends"], 10, 64)
	high, _ := strconv.ParseInt(m["high"], 10, 64)
	seller, _ := uuid.Parse(m["seller"])
	bidder, _ := uuid.Parse(m["bidder"]) // "" parses to an error => uuid.Nil
	return snapshot{
		Round: int32(round), Seller: seller, Start: start, Incr: incr, Ends: time.UnixMilli(ends).UTC(),
		Closed: m["closed"] == "1", High: high, Bidder: bidder, Token: m["token"],
	}, true, nil
}

// headToken is the token of the bid Redis currently considers highest.
func (s *state) headToken(ctx context.Context, listingID uuid.UUID) (string, error) {
	return s.rdb.HGet(ctx, auctionKey(listingID), "token").Result()
}

func (s *state) remove(ctx context.Context, listingID uuid.UUID) error {
	return s.rdb.Del(ctx, auctionKey(listingID)).Err()
}

// minNext is the smallest acceptable next bid.
func (s snapshot) minNext() int64 {
	if s.High == 0 {
		return s.Start
	}
	return s.High + s.Incr
}
