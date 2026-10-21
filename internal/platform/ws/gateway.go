package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
)

const (
	pingInterval      = 25 * time.Second
	pongTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	readLimit         = 64 << 10
	sendBuffer        = 64
	maxSubscriptions  = 20
	StatusTokenExpiry = websocket.StatusCode(4001) // client should refresh its token and reconnect
)

// Handler processes one client frame. A non-nil result is sent back as an "ack";
// an error is sent back as an "error" frame (an *httpx.Error keeps its code).
type Handler func(ctx context.Context, c *Conn, data json.RawMessage) (any, error)

// Gateway runs WebSocket connections. Design for reconnects: the server keeps no
// per-connection state worth restoring; a client reconnects, re-subscribes, and
// fetches anything it missed over REST (e.g. messages ?after=<last id>).
type Gateway struct {
	hub      *Hub
	origins  []string
	log      *slog.Logger
	handlers map[string]Handler

	mu    sync.Mutex
	conns map[*Conn]struct{}
}

func NewGateway(hub *Hub, originPatterns []string, log *slog.Logger) *Gateway {
	return &Gateway{hub: hub, origins: originPatterns, log: log, handlers: map[string]Handler{}, conns: map[*Conn]struct{}{}}
}

// Shutdown tells every client to reconnect elsewhere. http.Server.Shutdown doesn't
// track hijacked connections, so this must be called explicitly.
func (g *Gateway) Shutdown() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for c := range g.conns {
		go func() {
			_ = c.ws.Close(websocket.StatusGoingAway, "server restarting, please reconnect")
			c.cancel()
		}()
	}
}

// Handle registers the handler for a client frame type. Call before serving.
func (g *Gateway) Handle(eventType string, h Handler) { g.handlers[eventType] = h }

// Conn is one live client connection.
type Conn struct {
	UserID uuid.UUID
	hub    *Hub
	ws     *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc

	mu       sync.Mutex
	channels map[string]struct{}
}

// Subscribe adds the connection to a pub/sub channel (e.g. a listing's bid stream).
func (c *Conn) Subscribe(ctx context.Context, channel string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.channels[channel]; ok {
		return nil
	}
	if len(c.channels) >= maxSubscriptions {
		return httpx.Conflict("too_many_subscriptions", "too many subscriptions on one connection")
	}
	if err := c.hub.subscribe(ctx, c, channel); err != nil {
		return err
	}
	c.channels[channel] = struct{}{}
	return nil
}

func (c *Conn) Unsubscribe(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.channels[channel]; ok {
		delete(c.channels, channel)
		c.hub.unsubscribe(c, channel)
	}
}

// enqueue never blocks the hub: a client too slow to drain its buffer is dropped
// and will reconnect.
func (c *Conn) enqueue(b []byte) {
	select {
	case c.send <- b:
	default:
		c.cancel()
	}
}

func (c *Conn) reply(ev Event) {
	b, err := json.Marshal(ev)
	if err == nil {
		c.enqueue(b)
	}
}

// Serve upgrades the request. The caller has already authenticated userID; the
// connection is closed when the access token expires so revocation takes effect.
func (g *Gateway) Serve(w http.ResponseWriter, r *http.Request, userID uuid.UUID, tokenExpiresAt time.Time) {
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: g.origins})
	if err != nil {
		g.log.InfoContext(r.Context(), "websocket upgrade failed", "err", err)
		return
	}
	wsConn.SetReadLimit(readLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &Conn{UserID: userID, hub: g.hub, ws: wsConn, send: make(chan []byte, sendBuffer), cancel: cancel, channels: map[string]struct{}{}}
	g.mu.Lock()
	g.conns[c] = struct{}{}
	g.mu.Unlock()
	defer g.cleanup(c)

	if err := c.Subscribe(ctx, UserChannel(userID)); err != nil {
		g.log.ErrorContext(ctx, "subscribe user channel failed", "err", err)
		_ = wsConn.Close(websocket.StatusInternalError, "subscribe failed")
		return
	}
	c.reply(Event{Type: "hello", Data: map[string]any{"user_id": userID, "server_time": time.Now().UTC(), "token_expires_at": tokenExpiresAt}})

	go g.writeLoop(ctx, c)
	expiry := time.AfterFunc(time.Until(tokenExpiresAt), func() {
		_ = wsConn.Close(StatusTokenExpiry, "access token expired")
		cancel()
	})
	defer expiry.Stop()

	g.readLoop(ctx, c)
}

func (g *Gateway) readLoop(ctx context.Context, c *Conn) {
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			return // closed by client, by us, or ctx cancelled
		}
		if typ != websocket.MessageText {
			c.reply(Event{Type: "error", Error: &ErrorBody{Code: "bad_frame", Message: "send JSON text frames"}})
			continue
		}
		var in struct {
			Type string          `json:"type"`
			ID   string          `json:"id"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(b, &in); err != nil || in.Type == "" {
			c.reply(Event{Type: "error", ID: in.ID, Error: &ErrorBody{Code: "bad_frame", Message: "frame must be {\"type\":..., \"id\":..., \"data\":...}"}})
			continue
		}
		if in.Type == "ping" {
			c.reply(Event{Type: "pong", ID: in.ID})
			continue
		}
		h, ok := g.handlers[in.Type]
		if !ok {
			c.reply(Event{Type: "error", ID: in.ID, Error: &ErrorBody{Code: "unknown_type", Message: "unknown frame type " + in.Type}})
			continue
		}
		res, err := h(ctx, c, in.Data)
		if err != nil {
			c.reply(Event{Type: "error", ID: in.ID, Error: errorBody(ctx, g.log, in.Type, err)})
			continue
		}
		c.reply(Event{Type: "ack", ID: in.ID, Data: res})
	}
}

func (g *Gateway) writeLoop(ctx context.Context, c *Conn) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-ping.C:
			// Ping needs the concurrent read loop to process the pong.
			pctx, cancel := context.WithTimeout(ctx, pongTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (g *Gateway) cleanup(c *Conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()

	c.mu.Lock()
	channels := make([]string, 0, len(c.channels))
	for ch := range c.channels {
		channels = append(channels, ch)
	}
	c.channels = map[string]struct{}{}
	c.mu.Unlock()
	for _, ch := range channels {
		c.hub.unsubscribe(c, ch)
	}
	_ = c.ws.CloseNow()
}

func errorBody(ctx context.Context, log *slog.Logger, frameType string, err error) *ErrorBody {
	var apiErr *httpx.Error
	if errors.As(err, &apiErr) && apiErr.Status < 500 {
		return &ErrorBody{Code: apiErr.Code, Message: apiErr.Message}
	}
	log.ErrorContext(ctx, "websocket handler failed", "type", frameType, "err", err)
	return &ErrorBody{Code: "internal", Message: "internal server error"}
}
