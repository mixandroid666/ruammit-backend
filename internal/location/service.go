package location

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"waymeet-backend/internal/platform/storage"
	"waymeet-backend/internal/platform/storage/dbgen"
)

// Search bounds and defaults (metres / counts / years).
const (
	minRadiusM     = 100     // 0.1 km
	maxRadiusM     = 500_000 // 500 km — matches the Flutter distance slider
	defaultRadiusM = 50_000  // 50 km
	maxLimit       = 100
	defaultLimit   = 50
	floorAge       = 13 // app minimum age
	ceilAge        = 120
)

// ErrInvalidCoordinates is returned for out-of-range or missing lat/lng.
var ErrInvalidCoordinates = errors.New("invalid coordinates")

// validGenders mirrors the user service; anything else means "no filter".
var validGenders = map[string]bool{"male": true, "female": true, "other": true}

// Service implements location updates and nearby-people discovery.
type Service struct {
	db  *storage.DB
	log *slog.Logger
}

// NewService wires the location service.
func NewService(db *storage.DB, log *slog.Logger) *Service {
	return &Service{db: db, log: log}
}

// NearbyUser is one discovered person, with distance from the viewer.
type NearbyUser struct {
	ID          string
	DisplayName string
	AvatarURL   string
	Bio         string
	Gender      string
	Age         int
	DistanceM   float64
}

// NearbyParams are the raw (pre-validation) search inputs.
type NearbyParams struct {
	Lat, Lng float64
	RadiusM  float64
	Gender   string // "" / male / female / other
	MinAge   int
	MaxAge   int
	Limit    int
}

// UpdateLocation stores the caller's current position so they appear in others'
// nearby searches and distances can be computed.
func (s *Service) UpdateLocation(ctx context.Context, userID string, lat, lng float64) error {
	if !validLatLng(lat, lng) {
		return ErrInvalidCoordinates
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return ErrInvalidCoordinates
	}
	return s.db.Queries.UpdateUserLocation(ctx, dbgen.UpdateUserLocationParams{
		Lng: lng,
		Lat: lat,
		ID:  uid,
	})
}

// Nearby returns people near the viewer, nearest first, applying defaults and
// caps to the supplied filters.
func (s *Service) Nearby(ctx context.Context, viewerID string, p NearbyParams) ([]NearbyUser, error) {
	if !validLatLng(p.Lat, p.Lng) {
		return nil, ErrInvalidCoordinates
	}
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return nil, ErrInvalidCoordinates
	}
	p = normalize(p)

	rows, err := s.db.Queries.ListNearbyUsers(ctx, dbgen.ListNearbyUsersParams{
		Lng:      p.Lng,
		Lat:      p.Lat,
		ViewerID: viewer,
		RadiusM:  p.RadiusM,
		Gender:   p.Gender,
		MinAge:   int32(p.MinAge),
		MaxAge:   int32(p.MaxAge),
		Lim:      int32(p.Limit),
	})
	if err != nil {
		return nil, err
	}

	out := make([]NearbyUser, 0, len(rows))
	for _, r := range rows {
		out = append(out, NearbyUser{
			ID:          uuidString(r.ID),
			DisplayName: deref(r.DisplayName),
			AvatarURL:   deref(r.AvatarUrl),
			Bio:         deref(r.Bio),
			Gender:      deref(r.Gender),
			Age:         int(r.Age),
			DistanceM:   r.DistanceM,
		})
	}
	return out, nil
}

// normalize applies defaults, caps and sanitises the filters.
func normalize(p NearbyParams) NearbyParams {
	if p.RadiusM <= 0 {
		p.RadiusM = defaultRadiusM
	}
	p.RadiusM = clampF(p.RadiusM, minRadiusM, maxRadiusM)

	if p.Limit <= 0 {
		p.Limit = defaultLimit
	}
	p.Limit = clampI(p.Limit, 1, maxLimit)

	if p.MinAge <= 0 {
		p.MinAge = floorAge
	}
	if p.MaxAge <= 0 {
		p.MaxAge = ceilAge
	}
	p.MinAge = clampI(p.MinAge, floorAge, ceilAge)
	p.MaxAge = clampI(p.MaxAge, floorAge, ceilAge)
	if p.MaxAge < p.MinAge {
		p.MinAge, p.MaxAge = p.MaxAge, p.MinAge
	}

	g := strings.ToLower(strings.TrimSpace(p.Gender))
	if !validGenders[g] {
		g = "" // unknown / "everyone" → no gender filter
	}
	p.Gender = g
	return p
}

// --- helpers ---------------------------------------------------------------

func validLatLng(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
