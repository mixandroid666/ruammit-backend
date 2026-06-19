package user

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"ruammit-backend/internal/platform/mediastore"
	"ruammit-backend/internal/platform/storage"
	"ruammit-backend/internal/platform/storage/dbgen"
)

// Profile field limits / rules.
const (
	minNameLen = 2
	maxNameLen = 50
	maxBioLen  = 300
	minAge     = 13
)

// Allowed gender values (match the discovery filter and the Flutter profile UI).
var validGenders = map[string]bool{"male": true, "female": true, "other": true}

// Domain errors mapped to HTTP status codes by the handler.
var (
	ErrInvalidName   = errors.New("invalid display name")
	ErrInvalidGender = errors.New("invalid gender")
	ErrInvalidBirth  = errors.New("invalid birth date")
	ErrUnderage      = errors.New("under the minimum age")
	ErrBioTooLong    = errors.New("bio too long")
	ErrNotFound      = errors.New("user not found")
)

// Service implements profile read/update.
type Service struct {
	db    *storage.DB
	media mediastore.Store
	log   *slog.Logger
}

// NewService wires the user service.
func NewService(db *storage.DB, media mediastore.Store, log *slog.Logger) *Service {
	return &Service{db: db, media: media, log: log}
}

// Profile is a user's public-facing profile.
type Profile struct {
	ID               string
	Email            string
	Phone            string
	DisplayName      string
	AvatarURL        string
	Bio              string
	Gender           string
	BirthDate        *time.Time
	Status           string
	ProfileCompleted bool
}

// NewAvatar is a decoded avatar upload ready to be stored.
type NewAvatar struct {
	Ext  string // ".jpg" | ".png" | ".webp"
	Data []byte
}

// UpdateProfileInput is the validated, decoded profile update. Pointer fields
// left nil are unchanged (partial update); BirthDate is a pre-parsed date.
type UpdateProfileInput struct {
	DisplayName *string
	Gender      *string
	Bio         *string
	BirthDate   *time.Time
	Avatar      *NewAvatar
}

// Get returns the caller's profile.
func (s *Service) Get(ctx context.Context, userID string) (*Profile, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}
	row, err := s.db.Queries.GetProfile(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return profileFromRow(getRow(row)), nil
}

// Update validates and applies a profile update, storing the avatar first when
// present, and returns the updated profile.
func (s *Service) Update(ctx context.Context, userID string, in UpdateProfileInput) (*Profile, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}

	params := dbgen.UpdateProfileParams{ID: uid}

	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if n := len([]rune(name)); n < minNameLen || n > maxNameLen {
			return nil, ErrInvalidName
		}
		params.DisplayName = &name
	}
	if in.Gender != nil {
		g := strings.ToLower(strings.TrimSpace(*in.Gender))
		if !validGenders[g] {
			return nil, ErrInvalidGender
		}
		params.Gender = &g
	}
	if in.Bio != nil {
		bio := strings.TrimSpace(*in.Bio)
		if len([]rune(bio)) > maxBioLen {
			return nil, ErrBioTooLong
		}
		params.Bio = &bio
	}
	if in.BirthDate != nil {
		bd := *in.BirthDate
		if bd.After(time.Now()) {
			return nil, ErrInvalidBirth
		}
		if ageOn(time.Now(), bd) < minAge {
			return nil, ErrUnderage
		}
		params.BirthDate = pgtype.Date{Time: bd, Valid: true}
	}

	if in.Avatar != nil {
		key := "avatars/" + userID + "/avatar" + in.Avatar.Ext
		url, err := s.media.Save(key, bytes.NewReader(in.Avatar.Data))
		if err != nil {
			return nil, err
		}
		params.AvatarUrl = &url
	}

	row, err := s.db.Queries.UpdateProfile(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return profileFromRow(profileRow(updateRow(row))), nil
}

// --- helpers ---------------------------------------------------------------

// profileRow is the common shape of GetProfileRow and UpdateProfileRow.
type profileRow struct {
	ID          pgtype.UUID
	Email       *string
	Phone       *string
	DisplayName *string
	AvatarUrl   *string
	Bio         *string
	Gender      *string
	BirthDate   pgtype.Date
	Status      string
}

func updateRow(r dbgen.UpdateProfileRow) profileRow {
	return profileRow{
		ID: r.ID, Email: r.Email, Phone: r.Phone, DisplayName: r.DisplayName,
		AvatarUrl: r.AvatarUrl, Bio: r.Bio, Gender: r.Gender,
		BirthDate: r.BirthDate, Status: r.Status,
	}
}

func getRow(r dbgen.GetProfileRow) profileRow {
	return profileRow{
		ID: r.ID, Email: r.Email, Phone: r.Phone, DisplayName: r.DisplayName,
		AvatarUrl: r.AvatarUrl, Bio: r.Bio, Gender: r.Gender,
		BirthDate: r.BirthDate, Status: r.Status,
	}
}

func profileFromRow(r profileRow) *Profile {
	p := &Profile{
		ID:               uuidString(r.ID),
		Email:            deref(r.Email),
		Phone:            deref(r.Phone),
		DisplayName:      deref(r.DisplayName),
		AvatarURL:        deref(r.AvatarUrl),
		Bio:              deref(r.Bio),
		Gender:           deref(r.Gender),
		Status:           r.Status,
		ProfileCompleted: r.DisplayName != nil && *r.DisplayName != "",
	}
	if r.BirthDate.Valid {
		t := r.BirthDate.Time
		p.BirthDate = &t
	}
	return p
}

// ageOn returns the age in whole years at instant `now` for birth date `bd`.
func ageOn(now, bd time.Time) int {
	years := now.Year() - bd.Year()
	if now.Month() < bd.Month() ||
		(now.Month() == bd.Month() && now.Day() < bd.Day()) {
		years--
	}
	return years
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	const hexDigits = "0123456789abcdef"
	dst := make([]byte, 36)
	pos := 0
	for i := range 16 {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			dst[pos] = '-'
			pos++
		}
		dst[pos] = hexDigits[b[i]>>4]
		dst[pos+1] = hexDigits[b[i]&0x0f]
		pos += 2
	}
	return string(dst)
}
