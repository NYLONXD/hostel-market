// Package ws is the realtime layer: a Hub that fans Redis pub/sub messages out to the
// WebSocket connections on this instance, and a Gateway that runs those connections
// (auth, heartbeats, typed frames). Because every publish goes through Redis, events
// reach users connected to any API instance.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// UserChannel carries events for one user (chat messages, order updates).
func UserChannel(id uuid.UUID) string { return "ws:user:" + id.String() }

// ListingChannel carries live bid events for one listing.
func ListingChannel(id uuid.UUID) string { return "ws:listing:" + id.String() }

// Event is the envelope for every frame in both directions.
type Event struct {
	Type  string     `json:"type"`
	ID    string     `json:"id,omitempty"` // client request id, echoed on ack/error
	Data  any        `json:"data,omitempty"`
	Error *ErrorBody `json:"error,omitempty"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Hub struct {
	rdb *redis.Client
	ps  *redis.PubSub // one dedicated Redis connection per instance
	log *slog.Logger

	mu   sync.RWMutex
	subs map[string]map[*Conn]struct{}
}

func NewHub(ctx context.Context, rdb *redis.Client, log *slog.Logger) *Hub {
	return &Hub{
		rdb:  rdb,
		ps:   rdb.Subscribe(ctx), // no channels yet; subscribed on demand
		log:  log,
		subs: map[string]map[*Conn]struct{}{},
	}
}

// Run delivers pub/sub messages until ctx ends. Start it once per process.
func (h *Hub) Run(ctx context.Context) {
	ch := h.ps.Channel(redis.WithChannelHealthCheckInterval(30 * time.Second))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			payload := []byte(msg.Payload)
			h.mu.RLock()
			for c := range h.subs[msg.Channel] {
				c.enqueue(payload)
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Close() error { return h.ps.Close() }

// Publish sends an event to every subscriber of channel on every instance.
func (h *Hub) Publish(ctx context.Context, channel string, ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return h.rdb.Publish(ctx, channel, b).Err()
}

// PublishToUsers is a best-effort fan-out to users' personal channels. Delivery is
// live-only: anything persistent is in Postgres and clients re-fetch it on reconnect.
func (h *Hub) PublishToUsers(ctx context.Context, userIDs []uuid.UUID, eventType string, data any) {
	ev := Event{Type: eventType, Data: data}
	for _, id := range userIDs {
		if err := h.Publish(context.WithoutCancel(ctx), UserChannel(id), ev); err != nil {
			h.log.WarnContext(ctx, "publish failed", "type", eventType, "err", err)
		}
	}
}

func (h *Hub) subscribe(ctx context.Context, c *Conn, channel string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[channel]
	if set == nil {
		if err := h.ps.Subscribe(ctx, channel); err != nil {
			return err
		}
		set = map[*Conn]struct{}{}
		h.subs[channel] = set
	}
	set[c] = struct{}{}
	return nil
}

func (h *Hub) unsubscribe(c *Conn, channel string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[channel]
	delete(set, c)
	if len(set) == 0 && set != nil {
		delete(h.subs, channel)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.ps.Unsubscribe(ctx, channel); err != nil {
			h.log.Warn("pubsub unsubscribe failed", "channel", channel, "err", err)
		}
	}
}
