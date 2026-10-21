// Package chat is buyer-seller messaging scoped to a listing. Every message is
// persisted in Postgres first; Redis pub/sub only delivers it live.
package chat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hostelmarket/internal/chat/store"
	"hostelmarket/internal/listings"
	"hostelmarket/internal/platform/db"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/users"
)

const (
	MaxBodyRunes    = 2000
	DefaultPageSize = 50
	MaxPageSize     = 100
)

type ListingReader interface {
	GetStrong(ctx context.Context, id uuid.UUID) (listings.Listing, error)
}

type UserReader interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
}

type Publisher interface {
	PublishToUsers(ctx context.Context, userIDs []uuid.UUID, eventType string, data any)
}

// Limiter throttles message sending (shared by REST and WebSocket).
type Limiter interface {
	Enforce(ctx context.Context, key string) error
}

type Service struct {
	db       *db.Router
	listings ListingReader
	users    UserReader
	pub      Publisher
	limit    Limiter
}

func NewService(router *db.Router, l ListingReader, u UserReader, pub Publisher, limit Limiter) *Service {
	return &Service{db: router, listings: l, users: u, pub: pub, limit: limit}
}

type Conversation struct {
	ID            uuid.UUID  `json:"id"`
	ListingID     uuid.UUID  `json:"listing_id"`
	BuyerID       uuid.UUID  `json:"buyer_id"`
	SellerID      uuid.UUID  `json:"seller_id"`
	LastMessageAt *time.Time `json:"last_message_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Unread        int64      `json:"unread"`
	LastMessage   string     `json:"last_message,omitempty"`
	MyLastReadID  int64      `json:"my_last_read_id"`
	PeerLastRead  int64      `json:"peer_last_read_id"`
}

type Message struct {
	ID             int64     `json:"id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	SenderID       uuid.UUID `json:"sender_id"`
	ClientMsgID    uuid.UUID `json:"client_msg_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}

var errNotFound = httpx.NotFound("conversation not found")

// Open starts (or returns the existing) conversation between a buyer and a listing's seller.
func (s *Service) Open(ctx context.Context, buyerID, listingID uuid.UUID) (Conversation, error) {
	l, err := s.listings.GetStrong(ctx, listingID)
	if errors.Is(err, listings.ErrNotFound) || (err == nil && l.Status == listings.StatusRemoved) {
		return Conversation{}, httpx.NotFound("listing not found")
	}
	if err != nil {
		return Conversation{}, httpx.Internal(err)
	}
	if l.SellerID == buyerID {
		return Conversation{}, httpx.Conflict("own_listing", "buyers start conversations; open one from the buyer's side")
	}
	buyer, err := s.users.Get(ctx, buyerID)
	if err != nil {
		return Conversation{}, httpx.Internal(err)
	}
	switch {
	case !buyer.IsVerified:
		return Conversation{}, httpx.Forbidden("not_verified", "verify your account before messaging sellers")
	case !strings.EqualFold(buyer.Hostel, l.Hostel):
		return Conversation{}, httpx.Forbidden("different_hostel", "this listing is only open to residents of "+l.Hostel)
	}

	var row store.Conversation
	err = s.db.WithTx(ctx, func(tx pgx.Tx) error {
		row, err = store.New(tx).UpsertConversation(ctx, store.UpsertConversationParams{ListingID: l.ID, BuyerID: buyerID, SellerID: l.SellerID})
		return err
	})
	if err != nil {
		return Conversation{}, httpx.Internal(fmt.Errorf("open conversation: %w", err))
	}
	return toConversation(row, buyerID), nil
}

// Send stores a message and pushes it to both participants. Retrying with the same
// client_msg_id returns the stored message instead of creating a duplicate.
func (s *Service) Send(ctx context.Context, senderID, convID, clientMsgID uuid.UUID, body string) (Message, error) {
	body = strings.TrimSpace(body)
	if n := len([]rune(body)); n == 0 || n > MaxBodyRunes {
		return Message{}, httpx.ValidationFailed(httpx.FieldError{Field: "body", Rule: "len", Message: "must be 1-2000 characters"})
	}
	if clientMsgID == uuid.Nil {
		return Message{}, httpx.ValidationFailed(httpx.FieldError{Field: "client_msg_id", Rule: "required", Message: "is required"})
	}
	if err := s.limit.Enforce(ctx, "u:"+senderID.String()); err != nil {
		return Message{}, err
	}

	var (
		msg       store.Message
		conv      store.Conversation
		duplicate bool
	)
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		if conv, err = s.participantConversation(ctx, q, senderID, convID); err != nil {
			return err
		}
		msg, err = q.InsertMessage(ctx, store.InsertMessageParams{ConversationID: convID, SenderID: senderID, ClientMsgID: clientMsgID, Body: body})
		if db.IsNoRows(err) { // ON CONFLICT DO NOTHING: already stored
			duplicate = true
			msg, err = q.GetMessageByClientID(ctx, store.GetMessageByClientIDParams{ConversationID: convID, SenderID: senderID, ClientMsgID: clientMsgID})
			return err
		}
		if err != nil {
			return err
		}
		return q.TouchConversation(ctx, store.TouchConversationParams{ID: convID, At: &msg.CreatedAt, SenderID: senderID, MessageID: msg.ID})
	})
	if err != nil {
		return Message{}, asAPIError(err)
	}
	m := toMessage(msg)
	if !duplicate {
		s.pub.PublishToUsers(ctx, []uuid.UUID{conv.BuyerID, conv.SellerID}, "chat.message", m)
	}
	return m, nil
}

// History pages messages oldest-first. With after set it returns what came after that
// id (reconnect catch-up); otherwise the page ending before `before` (or the latest).
// Chat is read from the primary so catch-up never misses a just-delivered message.
func (s *Service) History(ctx context.Context, userID, convID uuid.UUID, before, after *int64, limit int32) ([]Message, bool, error) {
	q := store.New(s.db.Reader(ctx, db.Strong))
	if _, err := s.participantConversation(ctx, q, userID, convID); err != nil {
		return nil, false, err
	}
	var (
		rows []store.Message
		err  error
	)
	if after != nil {
		rows, err = q.ListMessagesAfter(ctx, store.ListMessagesAfterParams{ConversationID: convID, AfterID: *after, RowLimit: limit + 1})
	} else {
		rows, err = q.ListMessagesBefore(ctx, store.ListMessagesBeforeParams{ConversationID: convID, BeforeID: before, RowLimit: limit + 1})
	}
	if err != nil {
		return nil, false, httpx.Internal(err)
	}
	hasMore := len(rows) > int(limit)
	if hasMore {
		rows = rows[:limit]
	}
	if after == nil {
		slices.Reverse(rows) // fetched newest-first
	}
	out := make([]Message, len(rows))
	for i, r := range rows {
		out[i] = toMessage(r)
	}
	return out, hasMore, nil
}

// MarkRead moves the caller's read pointer forward and tells the other side (read receipts).
func (s *Service) MarkRead(ctx context.Context, userID, convID uuid.UUID, upTo int64) (Conversation, error) {
	var row store.Conversation
	err := s.db.WithTx(ctx, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := s.participantConversation(ctx, q, userID, convID); err != nil {
			return err
		}
		var err error
		row, err = q.MarkRead(ctx, store.MarkReadParams{ID: convID, UserID: userID, UpTo: upTo})
		return err
	})
	if err != nil {
		return Conversation{}, asAPIError(err)
	}
	c := toConversation(row, userID)
	peer := row.BuyerID
	if peer == userID {
		peer = row.SellerID
	}
	s.pub.PublishToUsers(ctx, []uuid.UUID{peer}, "chat.read", map[string]any{
		"conversation_id": convID, "reader_id": userID, "last_read_id": c.MyLastReadID,
	})
	return c, nil
}

type ConversationCursor struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"i"`
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, after *ConversationCursor, limit int32) ([]Conversation, *ConversationCursor, error) {
	p := store.ListConversationsParams{UserID: userID, RowLimit: limit + 1}
	if after != nil {
		p.AfterAt, p.AfterID = &after.At, &after.ID
	}
	rows, err := store.New(s.db.Reader(ctx, db.Strong)).ListConversations(ctx, p)
	if err != nil {
		return nil, nil, httpx.Internal(err)
	}
	out := make([]Conversation, 0, len(rows))
	for _, r := range rows {
		c := toConversation(r.Conversation, userID)
		c.Unread, c.LastMessage = r.Unread, r.LastBody
		out = append(out, c)
	}
	var next *ConversationCursor
	if len(out) > int(limit) {
		out = out[:limit]
		last := out[len(out)-1]
		at := last.CreatedAt
		if last.LastMessageAt != nil {
			at = *last.LastMessageAt
		}
		next = &ConversationCursor{At: at, ID: last.ID}
	}
	return out, next, nil
}

func (s *Service) UnreadTotal(ctx context.Context, userID uuid.UUID) (int64, error) {
	n, err := store.New(s.db.Reader(ctx, db.Strong)).CountUnread(ctx, userID)
	if err != nil {
		return 0, httpx.Internal(err)
	}
	return n, nil
}

// participantConversation loads a conversation the user takes part in; anyone else
// gets a 404 so conversation IDs reveal nothing.
func (s *Service) participantConversation(ctx context.Context, q *store.Queries, userID, convID uuid.UUID) (store.Conversation, error) {
	c, err := q.GetConversation(ctx, convID)
	if db.IsNoRows(err) {
		return store.Conversation{}, errNotFound
	}
	if err != nil {
		return store.Conversation{}, err
	}
	if c.BuyerID != userID && c.SellerID != userID {
		return store.Conversation{}, errNotFound
	}
	return c, nil
}

func toConversation(r store.Conversation, viewer uuid.UUID) Conversation {
	c := Conversation{ID: r.ID, ListingID: r.ListingID, BuyerID: r.BuyerID, SellerID: r.SellerID,
		LastMessageAt: r.LastMessageAt, CreatedAt: r.CreatedAt}
	if viewer == r.BuyerID {
		c.MyLastReadID, c.PeerLastRead = r.BuyerLastReadID, r.SellerLastReadID
	} else {
		c.MyLastReadID, c.PeerLastRead = r.SellerLastReadID, r.BuyerLastReadID
	}
	return c
}

func toMessage(r store.Message) Message {
	return Message{ID: r.ID, ConversationID: r.ConversationID, SenderID: r.SenderID, ClientMsgID: r.ClientMsgID, Body: r.Body, CreatedAt: r.CreatedAt}
}

func asAPIError(err error) error {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.Internal(err)
}
