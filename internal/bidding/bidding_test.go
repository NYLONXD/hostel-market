package bidding

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/media"
	"hostelmarket/internal/orders"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/testutil"
	"hostelmarket/internal/platform/ws"
	"hostelmarket/internal/users"
)

type photosOK struct{}

func (photosOK) VerifyOwned(context.Context, uuid.UUID, []string) ([]media.Asset, error) {
	return nil, nil
}
func (photosOK) ScheduleDelete(context.Context, []string) error { return nil }

type noCache struct{}

func (noCache) Get(context.Context, string, string, any) bool { return false }
func (noCache) Set(context.Context, string, string, any)      {}
func (noCache) Invalidate(context.Context, string)            {}

type recordingJobs struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingJobs) Enqueue(_ context.Context, t *asynq.Task, opts ...asynq.Option) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range opts {
		if o.Type() == asynq.TaskIDOpt {
			r.ids = append(r.ids, t.Type()+"|"+o.Value().(string))
		}
	}
	return nil
}

type recordingPub struct {
	mu     sync.Mutex
	events []string
}

func (p *recordingPub) Publish(_ context.Context, _ string, ev ws.Event) error {
	p.mu.Lock()
	p.events = append(p.events, ev.Type)
	p.mu.Unlock()
	return nil
}

func (p *recordingPub) PublishToUsers(_ context.Context, _ []uuid.UUID, typ string, _ any) {
	p.mu.Lock()
	p.events = append(p.events, typ)
	p.mu.Unlock()
}

type fixture struct {
	pool     *pgxpool.Pool
	rdb      *redis.Client
	clock    *clock.Manual
	users    *users.Service
	listings *listings.Service
	orders   *orders.Service
	svc      *Service
	jobs     *recordingJobs
	pub      *recordingPub
}

const (
	start = int64(10_000) // ₹100
	incr  = int64(500)    // ₹5
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rdb := testutil.Redis(t)
	pool := testutil.Postgres(t)
	router := testutil.Router(pool, nil)
	f := &fixture{pool: pool, rdb: rdb, clock: clock.NewManual(time.Now()), jobs: &recordingJobs{}, pub: &recordingPub{}}
	f.users = users.NewService(router)
	f.listings = listings.NewService(router, f.users, photosOK{}, noCache{}, f.clock)
	f.orders = orders.NewService(router, f.listings, f.users, f.jobs, nil, f.clock, 48*time.Hour)
	f.svc = NewService(router, rdb, f.listings, f.users, f.orders, f.pub, f.jobs, f.clock, Config{
		AntiSnipeWindow: 30 * time.Second, AntiSnipeExtension: 60 * time.Second, MaxPaymentDefaults: 2,
	})
	f.orders.EnableAuctions(f.svc, orders.AuctionConfig{FeeBps: 2000, PaymentDeadline: 30 * time.Minute, MaxPaymentDefaults: 2})
	f.listings.SetAuctionHooks(f.svc)
	return f
}

func (f *fixture) user(t *testing.T, mod ...func(*users.NewUser)) users.User {
	t.Helper()
	upi := "u@okaxis"
	nu := users.NewUser{Email: uuid.NewString() + "@x.in", PasswordHash: "h", Name: "U", Hostel: "H1", Block: "A", Year: 2, UPIID: &upi, Verified: true}
	for _, m := range mod {
		m(&nu)
	}
	u, err := f.users.Create(context.Background(), nu)
	require.NoError(t, err)
	return u
}

func (f *fixture) auction(t *testing.T, seller users.User, mod ...func(*listings.Draft)) listings.Listing {
	t.Helper()
	s, i, ends := start, incr, f.clock.Now().Add(24*time.Hour)
	d := listings.Draft{Title: "Study table", Category: "furniture", Condition: "good", BiddingEnabled: true,
		StartingPricePaise: &s, MinIncrementPaise: &i, BidEndsAt: &ends}
	for _, m := range mod {
		m(&d)
	}
	l, err := f.listings.Create(context.Background(), seller.ID, d)
	require.NoError(t, err)
	return l
}

// endNow moves the auction's end into the past in Postgres and Redis.
func (f *fixture) endNow(t *testing.T, id uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), `UPDATE listings SET bid_ends_at = now() - interval '1 second' WHERE id = $1`, id)
	require.NoError(t, err)
	f.svc.resync(context.Background(), id)
}

func apiCode(err error) string {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr, "got %v", err)
	assert.Equal(t, status, apiErr.Status, apiErr.Message)
	assert.Equal(t, code, apiErr.Code)
}

// TestHundredConcurrentBidders: 100 bidders fire at once with amounts
// start, start+incr, ..., start+99*incr. Whatever the interleaving, every persisted bid
// must beat the previous one by at least the increment, nothing that was rejected may
// be persisted, and the highest offer must win.
func TestHundredConcurrentBidders(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.auction(t, f.user(t))

	bidders := make([]users.User, 100)
	for i := range bidders {
		bidders[i] = f.user(t)
	}

	type result struct {
		amount int64
		err    error
	}
	results := make(chan result, len(bidders))
	startGun := make(chan struct{})
	var wg sync.WaitGroup
	for i, b := range bidders {
		amount := start + int64(i)*incr
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGun
			_, err := f.svc.PlaceBid(ctx, b.ID, l.ID, amount)
			results <- result{amount, err}
		}()
	}
	close(startGun)
	wg.Wait()
	close(results)

	accepted := map[int64]bool{}
	for r := range results {
		switch {
		case r.err == nil:
			accepted[r.amount] = true
		case apiCode(r.err) == "bid_too_low" || apiCode(r.err) == "outbid":
		default:
			t.Errorf("bid %d: unexpected error %v", r.amount, r.err)
		}
	}
	require.NotEmpty(t, accepted)

	rows, err := f.pool.Query(ctx, `SELECT amount_paise FROM bids WHERE listing_id = $1 ORDER BY id`, l.ID)
	require.NoError(t, err)
	var persisted []int64
	for rows.Next() {
		var a int64
		require.NoError(t, rows.Scan(&a))
		persisted = append(persisted, a)
	}
	require.NoError(t, rows.Err())

	assert.Len(t, persisted, len(accepted), "exactly the accepted bids are persisted")
	prev := int64(0)
	for i, a := range persisted {
		assert.True(t, accepted[a], "persisted bid %d was reported as rejected", a)
		if i == 0 {
			assert.GreaterOrEqual(t, a, start)
		} else {
			assert.GreaterOrEqual(t, a, prev+incr, "bid %d must beat %d by the increment", a, prev)
		}
		prev = a
	}

	maxBid := start + 99*incr
	assert.Equal(t, maxBid, persisted[len(persisted)-1], "the highest offer always wins")

	got, err := f.listings.GetStrong(ctx, l.ID)
	require.NoError(t, err)
	assert.Equal(t, maxBid, *got.Auction.CurrentBidPaise)
	assert.EqualValues(t, len(persisted), got.Auction.BidCount)

	live, err := f.svc.LiveAuction(ctx, got)
	require.NoError(t, err)
	assert.Equal(t, maxBid, *live.HighestBidPaise, "Redis agrees with Postgres")
	assert.Equal(t, maxBid+incr, live.MinNextBidPaise)
}

func TestBidRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, alice, bob := f.user(t), f.user(t), f.user(t)
	l := f.auction(t, seller)

	_, err := f.svc.PlaceBid(ctx, seller.ID, l.ID, start)
	requireCode(t, err, http.StatusForbidden, "own_listing")
	_, err = f.svc.PlaceBid(ctx, alice.ID, l.ID, start-1)
	requireCode(t, err, http.StatusConflict, "bid_too_low")

	b, err := f.svc.PlaceBid(ctx, alice.ID, l.ID, start)
	require.NoError(t, err, "first bid may equal the starting price")
	assert.Equal(t, start+incr, b.MinNextBidPaise)

	_, err = f.svc.PlaceBid(ctx, alice.ID, l.ID, start+5*incr)
	requireCode(t, err, http.StatusConflict, "already_highest")
	_, err = f.svc.PlaceBid(ctx, bob.ID, l.ID, start+incr-1)
	requireCode(t, err, http.StatusConflict, "bid_too_low")
	_, err = f.svc.PlaceBid(ctx, bob.ID, l.ID, start+incr)
	require.NoError(t, err)

	// Eligibility is enforced on bids too.
	blockB := "B"
	restricted := f.auction(t, seller, func(d *listings.Draft) { d.Rules.AllowedBlock = &blockB })
	_, err = f.svc.PlaceBid(ctx, alice.ID, restricted.ID, start)
	requireCode(t, err, http.StatusForbidden, "block_restricted")

	// Repeat non-payers are blocked.
	_, err = f.pool.Exec(ctx, `UPDATE users SET payment_defaults = 2 WHERE id = $1`, bob.ID)
	require.NoError(t, err)
	_, err = f.svc.PlaceBid(ctx, bob.ID, l.ID, start+10*incr)
	requireCode(t, err, http.StatusForbidden, "bidding_blocked")

	// Fixed-price listings don't take bids.
	price := int64(1000)
	fixed, err := f.listings.Create(ctx, seller.ID, listings.Draft{Title: "Kettle", Category: "appliances", Condition: "good", PricePaise: &price})
	require.NoError(t, err)
	_, err = f.svc.PlaceBid(ctx, alice.ID, fixed.ID, 2000)
	requireCode(t, err, http.StatusConflict, "not_an_auction")

	// After the end, bids are refused.
	f.endNow(t, l.ID)
	_, err = f.svc.PlaceBid(ctx, f.user(t).ID, l.ID, start+20*incr)
	requireCode(t, err, http.StatusConflict, "auction_closed")
}

func TestAntiSnipingExtendsEndTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.auction(t, f.user(t))

	// Ten seconds left.
	_, err := f.pool.Exec(ctx, `UPDATE listings SET bid_ends_at = date_trunc('second', now()) + interval '10 seconds' WHERE id = $1`, l.ID)
	require.NoError(t, err)
	f.svc.resync(ctx, l.ID)
	before, err := f.listings.GetStrong(ctx, l.ID)
	require.NoError(t, err)

	b, err := f.svc.PlaceBid(ctx, f.user(t).ID, l.ID, start)
	require.NoError(t, err)
	assert.True(t, b.Extended)
	assert.WithinDuration(t, before.Auction.EndsAt.Add(60*time.Second), b.EndsAt, time.Millisecond)

	after, err := f.listings.GetStrong(ctx, l.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, b.EndsAt, after.Auction.EndsAt, time.Millisecond, "Postgres has the extended end")

	// The close job was rescheduled for the new end time.
	f.jobs.mu.Lock()
	defer f.jobs.mu.Unlock()
	assert.Contains(t, f.jobs.ids, TaskCloseAuction+"|auction-close:"+l.ID.String()+":1:"+itoa(b.EndsAt.UnixMilli()))

	// A bid with plenty of time left doesn't extend.
	_, err = f.pool.Exec(ctx, `UPDATE listings SET bid_ends_at = now() + interval '1 hour' WHERE id = $1`, l.ID)
	require.NoError(t, err)
	f.svc.resync(ctx, l.ID)
	b2, err := f.svc.PlaceBid(ctx, f.user(t).ID, l.ID, start+incr)
	require.NoError(t, err)
	assert.False(t, b2.Extended)
}

func TestCloseAuctionIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, alice, bob := f.user(t), f.user(t), f.user(t)
	l := f.auction(t, seller)
	_, err := f.svc.PlaceBid(ctx, alice.ID, l.ID, start)
	require.NoError(t, err)
	_, err = f.svc.PlaceBid(ctx, bob.ID, l.ID, 12_345)
	require.NoError(t, err)

	// Not due yet: nothing happens.
	require.NoError(t, f.svc.CloseAuction(ctx, l.ID, 1))
	got, _ := f.listings.GetStrong(ctx, l.ID)
	assert.Nil(t, got.Auction.ClosedAt)

	f.endNow(t, l.ID)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- f.svc.CloseAuction(ctx, l.ID, 1)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, f.svc.CloseAuction(ctx, l.ID, 1), "running again later is a no-op")

	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE listing_id = $1`, l.ID).Scan(&n))
	assert.Equal(t, 1, n, "exactly one winning order")

	list, _, err := f.orders.List(ctx, bob.ID, orders.ListFilter{AsBuyer: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, list, 1)
	o := list[0]
	assert.Equal(t, orders.StatusAwaitingPayment, o.Status)
	assert.Equal(t, orders.KindAuction, o.Kind)
	assert.EqualValues(t, 12_345, o.PricePaise)
	assert.EqualValues(t, 2000, o.PlatformFeeBps)
	assert.EqualValues(t, 2_469, o.PlatformFeePaise, "20% of 12345 = 2469")
	assert.EqualValues(t, 9_876, o.SellerPayoutPaise)
	require.NotNil(t, o.PaymentDueAt)

	got, _ = f.listings.GetStrong(ctx, l.ID)
	assert.Equal(t, listings.StatusReserved, got.Status)
	assert.NotNil(t, got.Auction.ClosedAt)

	_, err = f.svc.PlaceBid(ctx, f.user(t).ID, l.ID, 50_000)
	requireCode(t, err, http.StatusConflict, "listing_unavailable")
}

func TestUnpaidWinnerPassesToNextBidder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller, alice, bob := f.user(t), f.user(t), f.user(t)
	l := f.auction(t, seller)
	_, err := f.svc.PlaceBid(ctx, bob.ID, l.ID, start)
	require.NoError(t, err)
	_, err = f.svc.PlaceBid(ctx, alice.ID, l.ID, start+incr)
	require.NoError(t, err)
	_, err = f.svc.PlaceBid(ctx, bob.ID, l.ID, start+3*incr)
	require.NoError(t, err)
	f.endNow(t, l.ID)
	require.NoError(t, f.svc.CloseAuction(ctx, l.ID, 1))

	won, _, err := f.orders.List(ctx, bob.ID, orders.ListFilter{AsBuyer: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, won, 1)

	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, won[0].ID), "before the deadline: no-op")
	f.clock.Advance(31 * time.Minute)
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, won[0].ID))
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, won[0].ID), "idempotent")

	expired, err := f.orders.Get(ctx, bob.ID, won[0].ID)
	require.NoError(t, err)
	assert.Equal(t, orders.StatusExpired, expired.Status)
	bobNow, _ := f.users.Get(ctx, bob.ID)
	assert.EqualValues(t, 1, bobNow.PaymentDefaults, "non-payer is flagged")

	// Alice, the next distinct bidder, is offered the item at her own best bid.
	offered, _, err := f.orders.List(ctx, alice.ID, orders.ListFilter{AsBuyer: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, offered, 1)
	assert.EqualValues(t, start+incr, offered[0].PricePaise)
	assert.Equal(t, orders.StatusAwaitingPayment, offered[0].Status)

	// Alice doesn't pay either; Bob already defaulted, so nobody is left.
	f.clock.Advance(31 * time.Minute)
	require.NoError(t, f.orders.ExpireAuctionPayment(ctx, offered[0].ID))
	got, _ := f.listings.GetStrong(ctx, l.ID)
	assert.Equal(t, listings.StatusActive, got.Status, "back on sale")
	assert.NotNil(t, got.Auction.ClosedAt, "but the round stays closed until the seller relists")
}

func TestRevertOnlyTouchesItsOwnBid(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.auction(t, f.user(t))
	a, b := uuid.New(), uuid.New()

	res, err := f.svc.state.place(ctx, l.ID, a, start, "tok-a", 1, time.Second, time.Second)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// Revert of the head bid restores the previous (empty) state.
	require.NoError(t, f.svc.state.revert(ctx, l.ID, "tok-a", res.Prev))
	snap, _, err := f.svc.state.live(ctx, l.ID)
	require.NoError(t, err)
	assert.Zero(t, snap.High)

	resA, err := f.svc.state.place(ctx, l.ID, a, start, "tok-a2", 1, time.Second, time.Second)
	require.NoError(t, err)
	resB, err := f.svc.state.place(ctx, l.ID, b, start+incr, "tok-b", 1, time.Second, time.Second)
	require.NoError(t, err)
	require.Equal(t, "ok", resB.Status)

	// A's write "failed", but B already outbid it: B's state must survive.
	require.NoError(t, f.svc.state.revert(ctx, l.ID, "tok-a2", resA.Prev))
	snap, _, _ = f.svc.state.live(ctx, l.ID)
	assert.Equal(t, start+incr, snap.High)
	assert.Equal(t, b, snap.Bidder)
}

func TestRebuildStateFromPostgres(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	l := f.auction(t, f.user(t))
	bidder := f.user(t)
	_, err := f.svc.PlaceBid(ctx, bidder.ID, l.ID, start+2*incr)
	require.NoError(t, err)

	require.NoError(t, f.rdb.Del(ctx, auctionKey(l.ID)).Err()) // simulate a Redis flush
	require.NoError(t, f.svc.RebuildState(ctx))

	snap, ok, err := f.svc.state.live(ctx, l.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, start+2*incr, snap.High)
	assert.Equal(t, bidder.ID, snap.Bidder)

	// Bids keep working against the rebuilt state.
	_, err = f.svc.PlaceBid(ctx, f.user(t).ID, l.ID, start+3*incr)
	require.NoError(t, err)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
