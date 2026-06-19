package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"ruammit-backend/internal/platform/config"
	"ruammit-backend/internal/platform/storage"
	"ruammit-backend/internal/platform/storage/dbgen"
)

// Tunables for the registration OTP flow.
const (
	purposeRegistration = "registration"
	statusPending       = "pending_verification"
	statusActive        = "active"

	otpTTL         = 10 * time.Minute
	maxAttempts    = 5
	resendCooldown = 60 * time.Second
	minPasswordLen = 6
)

// Domain errors. The handler maps these to HTTP status codes.
var (
	ErrAlreadyVerified    = errors.New("account already verified")
	ErrNoPending          = errors.New("no pending registration for this contact")
	ErrInvalidCode        = errors.New("invalid verification code")
	ErrCodeExpired        = errors.New("verification code expired")
	ErrTooManyAttempts    = errors.New("too many incorrect attempts")
	ErrResendTooSoon      = errors.New("a code was sent recently; please wait")
	ErrWeakPassword       = errors.New("password too short")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNotVerified        = errors.New("account not verified")
)

// ContactType is how a user signs up: by email or phone.
type ContactType string

const (
	ContactEmail ContactType = "email"
	ContactPhone ContactType = "phone"
)

// Service implements the registration / verification / login use cases.
type Service struct {
	db         *storage.DB
	cfg        config.Config
	log        *slog.Logger
	sender     Sender
	tokens     *TokenService
	refreshTTL time.Duration
}

// NewService wires the auth service.
func NewService(db *storage.DB, cfg config.Config, log *slog.Logger, sender Sender) *Service {
	return &Service{
		db:         db,
		cfg:        cfg,
		log:        log,
		sender:     sender,
		tokens:     NewTokenService(cfg.JWTSecret, cfg.AccessTokenTTL),
		refreshTTL: cfg.RefreshTokenTTL,
	}
}

// Middleware exposes the access-token authentication middleware so other
// modules can protect their routes.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return s.tokens.Middleware(next)
}

// OTPChallenge is returned after register/resend so the client can show the
// verification screen. DebugCode is populated only outside production.
type OTPChallenge struct {
	Contact   string
	ExpiresAt time.Time
	DebugCode string
}

// RegisterInput is the validated, normalized register request.
type RegisterInput struct {
	Type     ContactType
	Contact  string // already normalized (lowercased email / digits-only phone)
	Password string
}

// Register creates (or re-arms) a pending account and issues an OTP.
//
// Idempotent for unverified accounts: re-registering an unverified contact
// updates the password and sends a fresh code. An already-verified contact
// returns ErrAlreadyVerified.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*OTPChallenge, error) {
	if len(in.Password) < minPasswordLen {
		return nil, ErrWeakPassword
	}

	code, err := generateOTP()
	if err != nil {
		return nil, err
	}
	codeHash, err := hashSecret(code)
	if err != nil {
		return nil, err
	}
	pwHash, err := hashSecret(in.Password)
	if err != nil {
		return nil, err
	}

	var challenge *OTPChallenge
	err = s.withTx(ctx, func(q *dbgen.Queries) error {
		userID, status, err := s.upsertPendingUser(ctx, q, in.Type, in.Contact, pwHash)
		if err != nil {
			return err
		}
		if status == statusActive {
			return ErrAlreadyVerified
		}
		if err := q.InvalidatePendingOTPs(ctx, dbgen.InvalidatePendingOTPsParams{
			UserID: userID, Purpose: purposeRegistration,
		}); err != nil {
			return err
		}
		otp, err := q.CreateOTP(ctx, dbgen.CreateOTPParams{
			UserID:    userID,
			Purpose:   purposeRegistration,
			CodeHash:  codeHash,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(otpTTL), Valid: true},
		})
		if err != nil {
			return err
		}
		challenge = &OTPChallenge{Contact: in.Contact, ExpiresAt: otp.ExpiresAt.Time}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := s.sender.SendOTP(ctx, in.Contact, code); err != nil {
		s.log.Error("failed to send otp", "err", err, "contact", in.Contact)
		// The account + code are already persisted; the user can resend.
	}
	if !s.cfg.IsProduction() {
		challenge.DebugCode = code
	}
	return challenge, nil
}

// VerifyInput is the validated verify request.
type VerifyInput struct {
	Type    ContactType
	Contact string
	Code    string
}

// VerifyOTP checks the code and, on success, activates the account and logs the
// user in by issuing a token pair (so they can proceed straight to profile
// setup without re-authenticating).
//
// Note: a wrong code increments the attempt counter in its own statement (not
// rolled back), while the success path activates the user atomically.
func (s *Service) VerifyOTP(ctx context.Context, in VerifyInput) (*TokenPair, error) {
	q := s.db.Queries

	userID, status, err := s.findUser(ctx, q, in.Type, in.Contact)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoPending
		}
		return nil, err
	}
	if status == statusActive {
		return nil, ErrAlreadyVerified
	}

	otp, err := q.GetLatestOTP(ctx, dbgen.GetLatestOTPParams{
		UserID: userID, Purpose: purposeRegistration,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoPending
		}
		return nil, err
	}
	if time.Now().After(otp.ExpiresAt.Time) {
		return nil, ErrCodeExpired
	}
	if otp.Attempts >= maxAttempts {
		return nil, ErrTooManyAttempts
	}
	if !checkSecret(otp.CodeHash, in.Code) {
		if err := q.IncrementOTPAttempts(ctx, otp.ID); err != nil {
			s.log.Error("failed to record otp attempt", "err", err)
		}
		return nil, ErrInvalidCode
	}

	var pair *TokenPair
	err = s.withTx(ctx, func(tq *dbgen.Queries) error {
		if err := tq.ConsumeOTP(ctx, otp.ID); err != nil {
			return err
		}
		if err := tq.ActivateUser(ctx, userID); err != nil {
			return err
		}
		p, err := s.issueTokens(ctx, tq, userID)
		if err != nil {
			return err
		}
		pair = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

// ResendInput is the validated resend request.
type ResendInput struct {
	Type    ContactType
	Contact string
}

// ResendOTP issues a fresh code for a pending account, enforcing a cooldown.
func (s *Service) ResendOTP(ctx context.Context, in ResendInput) (*OTPChallenge, error) {
	q := s.db.Queries

	userID, status, err := s.findUser(ctx, q, in.Type, in.Contact)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoPending
		}
		return nil, err
	}
	if status == statusActive {
		return nil, ErrAlreadyVerified
	}

	// Enforce a cooldown based on the most recent unconsumed code.
	if latest, err := q.GetLatestOTP(ctx, dbgen.GetLatestOTPParams{
		UserID: userID, Purpose: purposeRegistration,
	}); err == nil {
		if time.Since(latest.CreatedAt.Time) < resendCooldown {
			return nil, ErrResendTooSoon
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	code, err := generateOTP()
	if err != nil {
		return nil, err
	}
	codeHash, err := hashSecret(code)
	if err != nil {
		return nil, err
	}

	var challenge *OTPChallenge
	err = s.withTx(ctx, func(tq *dbgen.Queries) error {
		if err := tq.InvalidatePendingOTPs(ctx, dbgen.InvalidatePendingOTPsParams{
			UserID: userID, Purpose: purposeRegistration,
		}); err != nil {
			return err
		}
		otp, err := tq.CreateOTP(ctx, dbgen.CreateOTPParams{
			UserID:    userID,
			Purpose:   purposeRegistration,
			CodeHash:  codeHash,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(otpTTL), Valid: true},
		})
		if err != nil {
			return err
		}
		challenge = &OTPChallenge{Contact: in.Contact, ExpiresAt: otp.ExpiresAt.Time}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := s.sender.SendOTP(ctx, in.Contact, code); err != nil {
		s.log.Error("failed to send otp", "err", err, "contact", in.Contact)
	}
	if !s.cfg.IsProduction() {
		challenge.DebugCode = code
	}
	return challenge, nil
}

// --- login / session ------------------------------------------------------

// LoginInput is the validated, normalized login request.
type LoginInput struct {
	Type     ContactType
	Contact  string
	Password string
}

// TokenPair is what login/refresh/verify return to the client.
type TokenPair struct {
	UserID           string
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	// ProfileCompleted is false until the user finishes profile setup (sets a
	// display name). The client routes to the setup screen while it's false.
	ProfileCompleted bool
}

// Login verifies credentials and issues an access + refresh token pair.
//
// Returns ErrInvalidCredentials for unknown contact / wrong password / social-
// only accounts (no enumeration), and ErrNotVerified if the account exists and
// the password is correct but it has not completed OTP verification.
func (s *Service) Login(ctx context.Context, in LoginInput) (*TokenPair, error) {
	var (
		userID pgtype.UUID
		status string
		pwHash *string
	)
	switch in.Type {
	case ContactEmail:
		row, err := s.db.Queries.GetCredentialsByEmail(ctx, in.Contact)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrInvalidCredentials
			}
			return nil, err
		}
		userID, status, pwHash = row.ID, row.Status, row.PasswordHash
	case ContactPhone:
		row, err := s.db.Queries.GetCredentialsByPhone(ctx, in.Contact)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrInvalidCredentials
			}
			return nil, err
		}
		userID, status, pwHash = row.ID, row.Status, row.PasswordHash
	default:
		return nil, fmt.Errorf("unknown contact type %q", in.Type)
	}

	if pwHash == nil || !checkSecret(*pwHash, in.Password) {
		return nil, ErrInvalidCredentials
	}
	if status != statusActive {
		return nil, ErrNotVerified
	}
	return s.issueTokens(ctx, s.db.Queries, userID)
}

// Refresh rotates a refresh token: the presented token is revoked and a fresh
// pair is issued. Presenting an already-revoked token is treated as possible
// theft and revokes all of the user's sessions.
func (s *Service) Refresh(ctx context.Context, rawToken string) (*TokenPair, error) {
	row, err := s.db.Queries.GetRefreshToken(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}
	if row.RevokedAt.Valid {
		// Reuse of a revoked token — revoke everything for this user.
		if err := s.db.Queries.RevokeAllUserRefreshTokens(ctx, row.UserID); err != nil {
			s.log.Error("failed to revoke sessions after token reuse", "err", err)
		}
		return nil, ErrInvalidToken
	}
	if time.Now().After(row.ExpiresAt.Time) {
		return nil, ErrInvalidToken
	}

	var pair *TokenPair
	err = s.withTx(ctx, func(q *dbgen.Queries) error {
		if err := q.RevokeRefreshToken(ctx, row.ID); err != nil {
			return err
		}
		p, err := s.issueTokens(ctx, q, row.UserID)
		if err != nil {
			return err
		}
		pair = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pair, nil
}

// Logout revokes a refresh token. Idempotent — an unknown token is a no-op.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	row, err := s.db.Queries.GetRefreshToken(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return s.db.Queries.RevokeRefreshToken(ctx, row.ID)
}

// issueTokens mints an access token and persists a new refresh token using the
// given Queries (pass a tx-bound Queries to make rotation atomic).
func (s *Service) issueTokens(ctx context.Context, q *dbgen.Queries, userID pgtype.UUID) (*TokenPair, error) {
	uid := uuidToString(userID)
	access, accessExp, err := s.tokens.MintAccess(uid)
	if err != nil {
		return nil, err
	}
	refresh, err := generateRefreshToken()
	if err != nil {
		return nil, err
	}
	refreshExp := time.Now().Add(s.refreshTTL)
	if _, err := q.CreateRefreshToken(ctx, dbgen.CreateRefreshTokenParams{
		UserID:    userID,
		TokenHash: hashToken(refresh),
		ExpiresAt: pgtype.Timestamptz{Time: refreshExp, Valid: true},
	}); err != nil {
		return nil, err
	}

	// A user has completed setup once they have a display name. Read it from the
	// same Queries (tx-bound where applicable) so verify-on-activation is correct.
	profileCompleted := false
	if u, err := q.GetUserByID(ctx, userID); err == nil {
		profileCompleted = u.DisplayName != nil && *u.DisplayName != ""
	}

	return &TokenPair{
		UserID:           uid,
		AccessToken:      access,
		AccessExpiresAt:  accessExp,
		RefreshToken:     refresh,
		RefreshExpiresAt: refreshExp,
		ProfileCompleted: profileCompleted,
	}, nil
}

// upsertPendingUser finds an existing user by contact or creates a pending one,
// returning the user id and current status. For a pending user it refreshes the
// password (the caller treats statusActive as ErrAlreadyVerified).
func (s *Service) upsertPendingUser(
	ctx context.Context, q *dbgen.Queries, t ContactType, contact, pwHash string,
) (pgtype.UUID, string, error) {
	switch t {
	case ContactEmail:
		u, err := q.FindUserByEmail(ctx, contact)
		if err == nil {
			if u.Status != statusActive {
				if err := q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{
					PasswordHash: pwHash, ID: u.ID,
				}); err != nil {
					return pgtype.UUID{}, "", err
				}
			}
			return u.ID, u.Status, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, "", err
		}
		created, err := q.CreatePendingUserByEmail(ctx, dbgen.CreatePendingUserByEmailParams{
			Email: contact, PasswordHash: pwHash,
		})
		return created.ID, created.Status, err

	case ContactPhone:
		u, err := q.FindUserByPhone(ctx, contact)
		if err == nil {
			if u.Status != statusActive {
				if err := q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{
					PasswordHash: pwHash, ID: u.ID,
				}); err != nil {
					return pgtype.UUID{}, "", err
				}
			}
			return u.ID, u.Status, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, "", err
		}
		created, err := q.CreatePendingUserByPhone(ctx, dbgen.CreatePendingUserByPhoneParams{
			Phone: contact, PasswordHash: pwHash,
		})
		return created.ID, created.Status, err
	}
	return pgtype.UUID{}, "", fmt.Errorf("unknown contact type %q", t)
}

func (s *Service) findUser(
	ctx context.Context, q *dbgen.Queries, t ContactType, contact string,
) (pgtype.UUID, string, error) {
	switch t {
	case ContactEmail:
		u, err := q.FindUserByEmail(ctx, contact)
		return u.ID, u.Status, err
	case ContactPhone:
		u, err := q.FindUserByPhone(ctx, contact)
		return u.ID, u.Status, err
	}
	return pgtype.UUID{}, "", fmt.Errorf("unknown contact type %q", t)
}

// withTx runs fn inside a transaction, committing on success.
func (s *Service) withTx(ctx context.Context, fn func(q *dbgen.Queries) error) error {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful commit
	if err := fn(s.db.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NormalizeEmail lowercases and trims an email for consistent storage/lookup.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// NormalizePhone keeps a leading '+' and digits only.
func NormalizePhone(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}
