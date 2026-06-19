package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"time"

	"ruammit-backend/internal/platform/httpx"
)

var phoneRe = regexp.MustCompile(`^\+?\d{6,15}$`)

// Handler exposes the auth HTTP endpoints.
type Handler struct {
	svc *Service
	log *slog.Logger
}

// NewHandler builds the auth handler.
func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes mounts the auth routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/verify-otp", h.verifyOTP)
	mux.HandleFunc("POST /api/v1/auth/resend-otp", h.resendOTP)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)

	// Example protected route: returns the caller's id from their access token.
	mux.Handle("GET /api/v1/auth/me", h.svc.Middleware(http.HandlerFunc(h.me)))
}

// --- request / response DTOs ----------------------------------------------

type registerRequest struct {
	ContactType string `json:"contact_type"` // "email" | "phone"
	Contact     string `json:"contact"`
	Password    string `json:"password"`
}

type verifyRequest struct {
	ContactType string `json:"contact_type"`
	Contact     string `json:"contact"`
	Code        string `json:"code"`
}

type resendRequest struct {
	ContactType string `json:"contact_type"`
	Contact     string `json:"contact"`
}

type challengeResponse struct {
	Message   string    `json:"message"`
	Contact   string    `json:"contact"`
	ExpiresAt time.Time `json:"expires_at"`
	// DebugCode is included only outside production, to ease client testing.
	DebugCode string `json:"debug_code,omitempty"`
}

type loginRequest struct {
	ContactType string `json:"contact_type"` // "email" | "phone"
	Contact     string `json:"contact"`
	Password    string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type userPayload struct {
	ID               string `json:"id"`
	ProfileCompleted bool   `json:"profile_completed"`
}

type tokenResponse struct {
	AccessToken      string      `json:"access_token"`
	TokenType        string      `json:"token_type"` // always "Bearer"
	ExpiresAt        time.Time   `json:"expires_at"` // access-token expiry
	RefreshToken     string      `json:"refresh_token"`
	RefreshExpiresAt time.Time   `json:"refresh_expires_at"`
	User             userPayload `json:"user"`
}

// --- handlers --------------------------------------------------------------

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decode(w, r, &req) {
		return
	}
	ct, contact, ok := h.validateContact(w, req.ContactType, req.Contact)
	if !ok {
		return
	}
	if len(req.Password) < minPasswordLen {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "Password must be at least 6 characters")
		return
	}

	challenge, err := h.svc.Register(r.Context(), RegisterInput{Type: ct, Contact: contact, Password: req.Password})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, challengeResponse{
		Message:   "Verification code sent.",
		Contact:   challenge.Contact,
		ExpiresAt: challenge.ExpiresAt,
		DebugCode: challenge.DebugCode,
	})
}

func (h *Handler) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decode(w, r, &req) {
		return
	}
	ct, contact, ok := h.validateContact(w, req.ContactType, req.Contact)
	if !ok {
		return
	}
	if len(req.Code) != 6 {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "Code must be 6 digits")
		return
	}

	pair, err := h.svc.VerifyOTP(r.Context(), VerifyInput{Type: ct, Contact: contact, Code: req.Code})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	// Verification logs the user in: return the token pair so the client can
	// proceed straight to profile setup.
	httpx.JSON(w, http.StatusOK, tokenResponseOf(pair))
}

func (h *Handler) resendOTP(w http.ResponseWriter, r *http.Request) {
	var req resendRequest
	if !decode(w, r, &req) {
		return
	}
	ct, contact, ok := h.validateContact(w, req.ContactType, req.Contact)
	if !ok {
		return
	}

	challenge, err := h.svc.ResendOTP(r.Context(), ResendInput{Type: ct, Contact: contact})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, challengeResponse{
		Message:   "A new verification code was sent.",
		Contact:   challenge.Contact,
		ExpiresAt: challenge.ExpiresAt,
		DebugCode: challenge.DebugCode,
	})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	ct, contact, ok := h.validateContact(w, req.ContactType, req.Contact)
	if !ok {
		return
	}
	if req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "Password is required")
		return
	}

	pair, err := h.svc.Login(r.Context(), LoginInput{Type: ct, Contact: contact, Password: req.Password})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tokenResponseOf(pair))
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decode(w, r, &req) {
		return
	}
	if req.RefreshToken == "" {
		httpx.Error(w, http.StatusBadRequest, "validation_error", "refresh_token is required")
		return
	}

	pair, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tokenResponseOf(pair))
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// me returns the authenticated caller's id (proves the access token works).
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id, ok := UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	httpx.JSON(w, http.StatusOK, userPayload{ID: id})
}

func tokenResponseOf(p *TokenPair) tokenResponse {
	return tokenResponse{
		AccessToken:      p.AccessToken,
		TokenType:        "Bearer",
		ExpiresAt:        p.AccessExpiresAt,
		RefreshToken:     p.RefreshToken,
		RefreshExpiresAt: p.RefreshExpiresAt,
		User:             userPayload{ID: p.UserID, ProfileCompleted: p.ProfileCompleted},
	}
}

// --- helpers ---------------------------------------------------------------

// decode reads a JSON body, rejecting unknown fields. Returns false (and writes
// an error) on failure.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Malformed JSON body")
		return false
	}
	return true
}

// validateContact validates the contact type + value and returns the parsed
// type and normalized contact. Writes an error and returns ok=false on failure.
func (h *Handler) validateContact(w http.ResponseWriter, rawType, rawContact string) (ContactType, string, bool) {
	switch ContactType(rawType) {
	case ContactEmail:
		contact := NormalizeEmail(rawContact)
		if _, err := mail.ParseAddress(contact); err != nil {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "Enter a valid email")
			return "", "", false
		}
		return ContactEmail, contact, true
	case ContactPhone:
		contact := NormalizePhone(rawContact)
		if !phoneRe.MatchString(contact) {
			httpx.Error(w, http.StatusBadRequest, "validation_error", "Enter a valid phone number")
			return "", "", false
		}
		return ContactPhone, contact, true
	default:
		httpx.Error(w, http.StatusBadRequest, "validation_error", `contact_type must be "email" or "phone"`)
		return "", "", false
	}
}

// writeServiceError maps domain errors to HTTP responses.
func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAlreadyVerified):
		httpx.Error(w, http.StatusConflict, "already_verified", "This account is already verified. Please log in.")
	case errors.Is(err, ErrNoPending):
		httpx.Error(w, http.StatusNotFound, "no_pending_registration", "No pending registration found for this contact.")
	case errors.Is(err, ErrInvalidCode):
		httpx.Error(w, http.StatusBadRequest, "invalid_code", "The verification code is incorrect.")
	case errors.Is(err, ErrCodeExpired):
		httpx.Error(w, http.StatusBadRequest, "code_expired", "The verification code has expired. Request a new one.")
	case errors.Is(err, ErrTooManyAttempts):
		httpx.Error(w, http.StatusTooManyRequests, "too_many_attempts", "Too many incorrect attempts. Request a new code.")
	case errors.Is(err, ErrResendTooSoon):
		httpx.Error(w, http.StatusTooManyRequests, "resend_cooldown", "Please wait a moment before requesting another code.")
	case errors.Is(err, ErrWeakPassword):
		httpx.Error(w, http.StatusBadRequest, "validation_error", "Password must be at least 6 characters")
	case errors.Is(err, ErrInvalidCredentials):
		httpx.Error(w, http.StatusUnauthorized, "invalid_credentials", "Incorrect email/phone or password.")
	case errors.Is(err, ErrNotVerified):
		httpx.Error(w, http.StatusForbidden, "account_not_verified", "Please verify your account before logging in.")
	case errors.Is(err, ErrInvalidToken):
		httpx.Error(w, http.StatusUnauthorized, "invalid_token", "Your session has expired. Please log in again.")
	default:
		h.log.Error("auth service error", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal_error", "")
	}
}
