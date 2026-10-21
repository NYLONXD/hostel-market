// Package logx builds the process logger. Every record logged with a request context
// automatically carries request_id and user_id.
package logx

import (
	"context"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"hostelmarket/internal/platform/reqctx"
)

// New returns a JSON logger in production and a human-readable one elsewhere.
func New(w io.Writer, env string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if env == "production" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(contextHandler{h})
}

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if info := reqctx.From(ctx); info != nil {
		if info.RequestID != "" {
			r.AddAttrs(slog.String("request_id", info.RequestID))
		}
		if info.UserID != uuid.Nil {
			r.AddAttrs(slog.String("user_id", info.UserID.String()))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
