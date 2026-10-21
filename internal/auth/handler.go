package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"hostelmarket/internal/platform/httpx"
	"hostelmarket/internal/platform/ratelimit"
	"hostelmarket/internal/platform/reqctx"
	"hostelmarket/internal/users"
)

type Handler struct {
	svc           *Service
	loginLimit    *ratelimit.Policy
	registerLimit *ratelimit.Policy
}

func NewHandler(svc *Service, loginLimit, registerLimit *ratelimit.Policy) *Handler {
	return &Handler{svc: svc, loginLimit: loginLimit, registerLimit: registerLimit}
}

// Routes registers the public auth endpoints (logout requires a token).
func (h *Handler) Routes(r chi.Router) {
	r.With(h.registerLimit.Middleware(ratelimit.ByIP)).Post("/auth/register", httpx.Handle(h.register))
	r.Post("/auth/login", httpx.Handle(h.login))
	r.Post("/auth/refresh", httpx.Handle(h.refresh))
	r.With(RequireUser).Post("/auth/logout", httpx.Handle(h.logout))
}

type sessionResponse struct {
	User                  users.MeView `json:"user"`
	TokenType             string       `json:"token_type"`
	AccessToken           string       `json:"access_token"`
	AccessTokenExpiresAt  time.Time    `json:"access_token_expires_at"`
	RefreshToken          string       `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time    `json:"refresh_token_expires_at"`
}

func writeSession(w http.ResponseWriter, status int, s Session) {
	httpx.WriteJSON(w, status, sessionResponse{
		User:                  users.NewMeView(s.User),
		TokenType:             "Bearer",
		AccessToken:           s.AccessToken,
		AccessTokenExpiresAt:  s.AccessExpiresAt,
		RefreshToken:          s.RefreshToken,
		RefreshTokenExpiresAt: s.RefreshExpiresAt,
	})
}

type registerRequest struct {
	Email    string  `json:"email" validate:"required,email,max=254"`
	Password string  `json:"password" validate:"required,min=8,max=72"` // bcrypt reads at most 72 bytes
	Name     string  `json:"name" validate:"required,min=1,max=100"`
	Hostel   string  `json:"hostel" validate:"required,min=1,max=100"`
	Block    string  `json:"block" validate:"required,min=1,max=20"`
	Year     int16   `json:"year" validate:"required,min=1,max=6"`
	UPIID    *string `json:"upi_id" validate:"omitnil,upi_id"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if len(req.Password) > 72 {
		return httpx.ValidationFailed(httpx.FieldError{Field: "password", Rule: "max", Param: "72", Message: "must be at most 72 bytes"})
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Hostel) == "" || strings.TrimSpace(req.Block) == "" {
		return httpx.ValidationFailed(httpx.FieldError{Field: "name/hostel/block", Rule: "required", Message: "must not be blank"})
	}
	sess, err := h.svc.Register(r.Context(), RegisterInput(req))
	if err != nil {
		return err
	}
	writeSession(w, http.StatusCreated, sess)
	return nil
}

type loginRequest struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,max=72"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	// Keyed by email+IP: slows password guessing against one account without locking a
	// whole hostel (which often shares one NAT IP) out of logging in.
	key := users.NormalizeEmail(req.Email) + "|" + reqctx.ClientIP(r.Context())
	if _, err := h.loginLimit.Check(r.Context(), key); err != nil {
		return err
	}
	sess, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	writeSession(w, http.StatusOK, sess)
	return nil
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	sess, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	writeSession(w, http.StatusOK, sess)
	return nil
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	var req logoutRequest
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &req); err != nil {
			return err
		}
	}
	p, _ := PrincipalFrom(r.Context())
	if err := h.svc.Logout(r.Context(), p, req.RefreshToken); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
