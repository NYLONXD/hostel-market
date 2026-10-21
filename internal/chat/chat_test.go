package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hostelmarket/internal/listings"
	"hostelmarket/internal/media"
	"hostelmarket/internal/platform/clock"
	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
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

type allow struct{}

func (allow) Enforce(context.Context, string) error { return nil }

type event struct {
	users []uuid.UUID
	typ   string
	data  any
}

type recorder struct {
	mu     sync.Mutex
	events []event
}

func (r *recorder) PublishToUsers(_ context.Context, ids []uuid.UUID, typ string, data any) {
	r.mu.Lock()
	r.events = append(r.events, event{ids, typ, data})
	r.mu.Unlock()
}

type fixture struct {
	users    *users.Service
	listings *listings.Service
	svc      *Service
	pub      *recorder
}

func newFixture(t *testing.T, pub Publisher) *fixture {
	t.Helper()
	router := testutil.Router(testutil.Postgres(t), nil)
	f := &fixture{pub: &recorder{}}
	if pub == nil {
		pub = f.pub
	}
	f.users = users.NewService(router)
	f.listings = listings.NewService(router, f.users, photosOK{}, noCache{}, clock.System{})
	f.svc = NewService(router, f.listings, f.users, pub, allow{})
	return f
}

func (f *fixture) user(t *testing.T, hostel string, verified bool) users.User {
	t.Helper()
	upi := "s@okaxis"
	u, err := f.users.Create(context.Background(), users.NewUser{Email: uuid.NewString() + "@x.in", PasswordHash: "h",
		Name: "S", Hostel: hostel, Block: "A", Year: 1, UPIID: &upi, Verified: verified})
	require.NoError(t, err)
	return u
}

func (f *fixture) listing(t *testing.T, seller users.User) listings.Listing {
	t.Helper()
	price := int64(5_000)
	l, err := f.listings.Create(context.Background(), seller.ID, listings.Draft{Title: "Lamp", Category: "other", Condition: "good", PricePaise: &price})
	require.NoError(t, err)
	return l
}

func requireAPIErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var apiErr *httpx.Error
	require.ErrorAs(t, err, &apiErr, "got %v", err)
	assert.Equal(t, status, apiErr.Status)
	assert.Equal(t, code, apiErr.Code)
}

func TestConversationRules(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	seller := f.user(t, "H1", true)
	l := f.listing(t, seller)

	_, err := f.svc.Open(ctx, seller.ID, l.ID)
	requireAPIErr(t, err, http.StatusConflict, "own_listing")
	_, err = f.svc.Open(ctx, f.user(t, "H2", true).ID, l.ID)
	requireAPIErr(t, err, http.StatusForbidden, "different_hostel")
	_, err = f.svc.Open(ctx, f.user(t, "H1", false).ID, l.ID)
	requireAPIErr(t, err, http.StatusForbidden, "not_verified")

	buyer := f.user(t, "H1", true)
	c1, err := f.svc.Open(ctx, buyer.ID, l.ID)
	require.NoError(t, err)
	c2, err := f.svc.Open(ctx, buyer.ID, l.ID)
	require.NoError(t, err)
	assert.Equal(t, c1.ID, c2.ID, "opening twice returns the same conversation")
}

func TestMessagingUnreadAndPrivacy(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	seller, buyer, stranger := f.user(t, "H1", true), f.user(t, "H1", true), f.user(t, "H1", true)
	l := f.listing(t, seller)
	conv, err := f.svc.Open(ctx, buyer.ID, l.ID)
	require.NoError(t, err)

	clientID := uuid.New()
	m1, err := f.svc.Send(ctx, buyer.ID, conv.ID, clientID, "  Is it still available?  ")
	require.NoError(t, err)
	assert.Equal(t, "Is it still available?", m1.Body)

	retry, err := f.svc.Send(ctx, buyer.ID, conv.ID, clientID, "Is it still available?")
	require.NoError(t, err)
	assert.Equal(t, m1.ID, retry.ID, "same client_msg_id is stored once")

	_, err = f.svc.Send(ctx, seller.ID, conv.ID, uuid.New(), "Yes!")
	require.NoError(t, err)
	_, err = f.svc.Send(ctx, seller.ID, conv.ID, uuid.New(), "Come to room 204")
	require.NoError(t, err)

	f.pub.mu.Lock()
	assert.Len(t, f.pub.events, 3, "duplicate retries are not re-broadcast")
	assert.ElementsMatch(t, []uuid.UUID{buyer.ID, seller.ID}, f.pub.events[0].users)
	f.pub.mu.Unlock()

	// Only the two participants can see or post.
	_, err = f.svc.Send(ctx, stranger.ID, conv.ID, uuid.New(), "hi")
	requireAPIErr(t, err, http.StatusNotFound, "not_found")
	_, _, err = f.svc.History(ctx, stranger.ID, conv.ID, nil, nil, 10)
	requireAPIErr(t, err, http.StatusNotFound, "not_found")

	n, err := f.svc.UnreadTotal(ctx, buyer.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "buyer has two unread replies")
	n, err = f.svc.UnreadTotal(ctx, seller.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// History: latest page oldest-first, then catch-up with ?after.
	msgs, more, err := f.svc.History(ctx, buyer.ID, conv.ID, nil, nil, 2)
	require.NoError(t, err)
	assert.True(t, more)
	require.Len(t, msgs, 2)
	assert.Equal(t, "Yes!", msgs[0].Body)
	after := m1.ID
	msgs, _, err = f.svc.History(ctx, buyer.ID, conv.ID, nil, &after, 10)
	require.NoError(t, err)
	assert.Len(t, msgs, 2)

	// Read pointers can't run past the newest message, and never move back.
	c, err := f.svc.MarkRead(ctx, buyer.ID, conv.ID, 1<<40)
	require.NoError(t, err)
	assert.Equal(t, msgs[1].ID, c.MyLastReadID)
	c, err = f.svc.MarkRead(ctx, buyer.ID, conv.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, msgs[1].ID, c.MyLastReadID)
	n, _ = f.svc.UnreadTotal(ctx, buyer.ID)
	assert.Zero(t, n)

	list, _, err := f.svc.List(ctx, seller.ID, nil, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "Come to room 204", list[0].LastMessage)
	assert.EqualValues(t, 1, list[0].Unread)
}

// TestWebSocketDelivery runs the real gateway over Redis pub/sub: the buyer sends a
// frame and the seller, on a separate connection, receives it live.
func TestWebSocketDelivery(t *testing.T) {
	rdb := testutil.Redis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := ws.NewHub(ctx, rdb, testutil.Logger())
	defer hub.Close()
	go hub.Run(ctx)

	f := newFixture(t, hub)
	seller, buyer := f.user(t, "H1", true), f.user(t, "H1", true)
	conv, err := f.svc.Open(ctx, buyer.ID, f.listing(t, seller).ID)
	require.NoError(t, err)

	gw := ws.NewGateway(hub, nil, testutil.Logger())
	NewHandler(f.svc).RegisterWS(gw)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := uuid.MustParse(r.URL.Query().Get("uid")) // stands in for the auth middleware
		rctx, _ := reqctx.New(r.Context(), "test")
		gw.Serve(w, r.WithContext(reqctx.SetUserID(rctx, uid)), uid, time.Now().Add(time.Hour))
	}))
	defer srv.Close()

	dial := func(uid uuid.UUID) *websocket.Conn {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"?uid="+uid.String(), nil) //nolint:bodyclose // coder/websocket owns the handshake response body
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.CloseNow() })
		require.Equal(t, "hello", readType(t, ctx, c).Type)
		return c
	}
	sellerConn, buyerConn := dial(seller.ID), dial(buyer.ID)

	frame, _ := json.Marshal(map[string]any{"type": "chat.send", "id": "r1", "data": map[string]any{
		"conversation_id": conv.ID, "client_msg_id": uuid.New(), "body": "Hi, still selling?",
	}})
	require.NoError(t, buyerConn.Write(ctx, websocket.MessageText, frame))

	got := waitFor(t, ctx, sellerConn, "chat.message")
	assert.Contains(t, string(got.Data), "Hi, still selling?")

	// The sender gets an ack for its request id (and its own echo, for other devices).
	ack := waitFor(t, ctx, buyerConn, "ack")
	assert.Equal(t, "r1", ack.ID)

	require.NoError(t, buyerConn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping","id":"p"}`)))
	assert.Equal(t, "p", waitFor(t, ctx, buyerConn, "pong").ID)

	require.NoError(t, buyerConn.Write(ctx, websocket.MessageText, []byte(`{"type":"nope"}`)))
	assert.Contains(t, string(waitFor(t, ctx, buyerConn, "error").Raw), "unknown_type")
}

type frame struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
	Raw  []byte          `json:"-"`
}

func readType(t *testing.T, ctx context.Context, c *websocket.Conn) frame {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, b, err := c.Read(rctx)
	require.NoError(t, err)
	var f frame
	require.NoError(t, json.Unmarshal(b, &f))
	f.Raw = b
	return f
}

func waitFor(t *testing.T, ctx context.Context, c *websocket.Conn, typ string) frame {
	t.Helper()
	for range 10 {
		if f := readType(t, ctx, c); f.Type == typ {
			return f
		}
	}
	t.Fatalf("no %q frame received", typ)
	return frame{}
}
