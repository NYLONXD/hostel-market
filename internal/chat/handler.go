package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/platform/ws"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers chat REST endpoints; the caller must require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/listings/{id}/conversations", httpx.Handle(h.open))
	r.Get("/conversations", httpx.Handle(h.list))
	r.Get("/conversations/unread-count", httpx.Handle(h.unread))
	r.Get("/conversations/{id}/messages", httpx.Handle(h.history))
	r.Post("/conversations/{id}/messages", httpx.Handle(h.send))
	r.Post("/conversations/{id}/read", httpx.Handle(h.read))
}

// RegisterWS adds the chat frames to the WebSocket gateway:
//
//	{"type":"chat.send","id":"r1","data":{"conversation_id":"...","client_msg_id":"...","body":"hi"}}
//	{"type":"chat.read","id":"r2","data":{"conversation_id":"...","last_read_message_id":42}}
func (h *Handler) RegisterWS(g *ws.Gateway) {
	g.Handle("chat.send", func(ctx context.Context, c *ws.Conn, data json.RawMessage) (any, error) {
		var in struct {
			ConversationID uuid.UUID `json:"conversation_id"`
			sendBody
		}
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, httpx.BadRequest("invalid_json", "data must be {conversation_id, client_msg_id, body}")
		}
		return h.svc.Send(ctx, c.UserID, in.ConversationID, in.ClientMsgID, in.Body)
	})
	g.Handle("chat.read", func(ctx context.Context, c *ws.Conn, data json.RawMessage) (any, error) {
		var in struct {
			ConversationID uuid.UUID `json:"conversation_id"`
			readBody
		}
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, httpx.BadRequest("invalid_json", "data must be {conversation_id, last_read_message_id}")
		}
		return h.svc.MarkRead(ctx, c.UserID, in.ConversationID, in.LastReadMessageID)
	})
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request) error {
	listingID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("listing not found")
	}
	uid, _ := reqctx.UserID(r.Context())
	c, err := h.svc.Open(r.Context(), uid, listingID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

type listResponse struct {
	Items      []Conversation `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, err := parseLimit(r.URL.Query().Get("limit"), 20)
	if err != nil {
		return err
	}
	var after *ConversationCursor
	if s := r.URL.Query().Get("cursor"); s != "" {
		var c ConversationCursor
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || json.Unmarshal(b, &c) != nil {
			return httpx.BadRequest("invalid_cursor", "cursor is invalid")
		}
		after = &c
	}
	uid, _ := reqctx.UserID(r.Context())
	items, next, err := h.svc.List(r.Context(), uid, after, limit)
	if err != nil {
		return err
	}
	resp := listResponse{Items: items}
	if next != nil {
		b, _ := json.Marshal(next)
		resp.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
	return nil
}

func (h *Handler) unread(w http.ResponseWriter, r *http.Request) error {
	uid, _ := reqctx.UserID(r.Context())
	n, err := h.svc.UnreadTotal(r.Context(), uid)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"unread": n})
	return nil
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) error {
	convID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	qv := r.URL.Query()
	limit, err := parseLimit(qv.Get("limit"), DefaultPageSize)
	if err != nil {
		return err
	}
	before, err := parseID(qv.Get("before"), "before")
	if err != nil {
		return err
	}
	after, err := parseID(qv.Get("after"), "after")
	if err != nil {
		return err
	}
	if before != nil && after != nil {
		return queryErr("after", "use either before or after, not both")
	}
	uid, _ := reqctx.UserID(r.Context())
	msgs, more, err := h.svc.History(r.Context(), uid, convID, before, after, limit)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": msgs, "has_more": more})
	return nil
}

type sendBody struct {
	ClientMsgID uuid.UUID `json:"client_msg_id" validate:"required"`
	Body        string    `json:"body" validate:"required,max=8000"`
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request) error {
	convID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var body sendBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	uid, _ := reqctx.UserID(r.Context())
	m, err := h.svc.Send(r.Context(), uid, convID, body.ClientMsgID, body.Body)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
	return nil
}

type readBody struct {
	LastReadMessageID int64 `json:"last_read_message_id" validate:"gte=0"`
}

func (h *Handler) read(w http.ResponseWriter, r *http.Request) error {
	convID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return errNotFound
	}
	var body readBody
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	uid, _ := reqctx.UserID(r.Context())
	c, err := h.svc.MarkRead(r.Context(), uid, convID, body.LastReadMessageID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

func parseLimit(s string, def int32) (int32, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxPageSize {
		return 0, queryErr("limit", "must be between 1 and 100")
	}
	return int32(n), nil
}

func parseID(s, field string) (*int64, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return nil, queryErr(field, "must be a message id")
	}
	return &n, nil
}

func queryErr(field, msg string) error {
	return httpx.ValidationFailed(httpx.FieldError{Field: field, Rule: "query", Message: msg})
}
