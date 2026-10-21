package users

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/reqctx"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers user endpoints. The caller must already require authentication.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/users/me", httpx.Handle(h.me))
	r.Patch("/users/me", httpx.Handle(h.updateMe))
	r.Get("/users/{id}", httpx.Handle(h.publicProfile))
}

// MeView is the caller's own profile.
type MeView struct {
	ID              uuid.UUID `json:"id"`
	Email           string    `json:"email"`
	Name            string    `json:"name"`
	Hostel          string    `json:"hostel"`
	Block           string    `json:"block"`
	Year            int16     `json:"year"`
	UPIID           *string   `json:"upi_id"`
	Role            Role      `json:"role"`
	IsVerified      bool      `json:"is_verified"`
	CanSell         bool      `json:"can_sell"`
	PaymentDefaults int32     `json:"payment_defaults"`
	CreatedAt       time.Time `json:"created_at"`
}

func NewMeView(u User) MeView {
	return MeView{
		ID: u.ID, Email: u.Email, Name: u.Name, Hostel: u.Hostel, Block: u.Block, Year: u.Year,
		UPIID: u.UPIID, Role: u.Role, IsVerified: u.IsVerified, CanSell: u.CanSell(),
		PaymentDefaults: u.PaymentDefaults, CreatedAt: u.CreatedAt,
	}
}

// PublicView is what other students may see: no email, no UPI ID.
type PublicView struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Hostel     string    `json:"hostel"`
	Block      string    `json:"block"`
	Year       int16     `json:"year"`
	IsVerified bool      `json:"is_verified"`
	CreatedAt  time.Time `json:"created_at"`
}

func NewPublicView(u User) PublicView {
	return PublicView{ID: u.ID, Name: u.Name, Hostel: u.Hostel, Block: u.Block, Year: u.Year,
		IsVerified: u.IsVerified, CreatedAt: u.CreatedAt}
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	uid, _ := reqctx.UserID(r.Context())
	u, err := h.svc.Get(r.Context(), uid)
	if err != nil {
		return mapErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, NewMeView(u))
	return nil
}

type updateMeRequest struct {
	Name  *string                `json:"name" validate:"omitnil,min=1,max=100"`
	Block *string                `json:"block" validate:"omitnil,min=1,max=20"`
	Year  *int16                 `json:"year" validate:"omitnil,min=1,max=6"`
	UPIID httpx.Optional[string] `json:"upi_id"` // null clears it
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) error {
	var req updateMeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" {
			return httpx.ValidationFailed(httpx.FieldError{Field: "name", Rule: "required", Message: "is required"})
		}
		req.Name = &n
	}
	patch := ProfilePatch{Name: req.Name, Block: req.Block, Year: req.Year}
	if req.UPIID.Set {
		patch.SetUPIID = true
		if !req.UPIID.Null {
			v := strings.TrimSpace(req.UPIID.Value)
			if !httpx.ValidUPIID(v) {
				return httpx.ValidationFailed(httpx.FieldError{Field: "upi_id", Rule: "upi_id", Message: "must be a UPI ID like name@bank"})
			}
			patch.UPIID = &v
		}
	}

	uid, _ := reqctx.UserID(r.Context())
	u, err := h.svc.UpdateProfile(r.Context(), uid, patch)
	if err != nil {
		return mapErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, NewMeView(u))
	return nil
}

func (h *Handler) publicProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return httpx.NotFound("user not found")
	}
	u, err := h.svc.GetForDisplay(r.Context(), id)
	if err != nil {
		return mapErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, NewPublicView(u))
	return nil
}

func mapErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("user not found")
	}
	return httpx.Internal(err)
}
