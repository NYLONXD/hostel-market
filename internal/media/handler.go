package media

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

// SellerLookup tells us whether the caller may upload listing photos.
type SellerLookup interface {
	Get(ctx context.Context, id uuid.UUID) (users.User, error)
}

type Handler struct {
	svc   *Service
	users SellerLookup
}

func NewHandler(svc *Service, u SellerLookup) *Handler { return &Handler{svc: svc, users: u} }

// Routes registers media endpoints; the caller must require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/media/upload-signature", httpx.Handle(h.sign))
}

func (h *Handler) sign(w http.ResponseWriter, r *http.Request) error {
	uid, _ := reqctx.UserID(r.Context())
	u, err := h.users.Get(r.Context(), uid)
	if errors.Is(err, users.ErrNotFound) {
		return httpx.Unauthorized("account no longer exists")
	}
	if err != nil {
		return httpx.Internal(err)
	}
	if !u.IsVerified {
		return httpx.Forbidden("not_verified", "verify your account before uploading listing photos")
	}
	sig, err := h.svc.SignUpload(uid)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, sig)
	return nil
}
