package location

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"waymeet-backend/internal/auth"
	"waymeet-backend/internal/platform/httpx"
)

// Handler exposes the location/discovery HTTP endpoints (all authenticated).
type Handler struct {
	svc  *Service
	auth *auth.Service
	log  *slog.Logger
}

// NewHandler builds the location handler.
func NewHandler(svc *Service, authSvc *auth.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, auth: authSvc, log: log}
}

// RegisterRoutes mounts the location routes on the given mux, behind auth.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("PUT /api/v1/users/me/location", h.auth.Middleware(http.HandlerFunc(h.updateLocation)))
	mux.Handle("GET /api/v1/users/nearby", h.auth.Middleware(http.HandlerFunc(h.nearby)))
}

// --- DTOs ------------------------------------------------------------------

type updateLocationRequest struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type nearbyUserDTO struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	AvatarURL   string  `json:"avatar_url"`
	Bio         string  `json:"bio"`
	Gender      string  `json:"gender"`
	Age         int     `json:"age"`
	DistanceM   float64 `json:"distance_m"`
}

// --- handlers --------------------------------------------------------------

// updateLocation stores the caller's current GPS position.
func (h *Handler) updateLocation(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	var req updateLocationRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Expected JSON with lat and lng.")
		return
	}
	if err := h.svc.UpdateLocation(r.Context(), userID, req.Lat, req.Lng); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nearby returns people near the viewer. Query params: lat, lng (required),
// radius_m, gender, min_age, max_age, limit (all optional).
func (h *Handler) nearby(w http.ResponseWriter, r *http.Request) {
	viewerID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "lat and lng are required.")
		return
	}

	users, err := h.svc.Nearby(r.Context(), viewerID, NearbyParams{
		Lat:     lat,
		Lng:     lng,
		RadiusM: queryFloat(q.Get("radius_m")),
		Gender:  q.Get("gender"),
		MinAge:  queryInt(q.Get("min_age")),
		MaxAge:  queryInt(q.Get("max_age")),
		Limit:   queryInt(q.Get("limit")),
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	dto := make([]nearbyUserDTO, 0, len(users))
	for _, u := range users {
		dto = append(dto, nearbyUserDTO{
			ID:          u.ID,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURL,
			Bio:         u.Bio,
			Gender:      u.Gender,
			Age:         u.Age,
			DistanceM:   u.DistanceM,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"users": dto})
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCoordinates):
		httpx.Error(w, http.StatusBadRequest, "invalid_coordinates", "The coordinates are invalid.")
	default:
		h.log.Error("location service error", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal_error", "")
	}
}

// queryFloat parses an optional float query param; 0 when absent/invalid (the
// service applies the default).
func queryFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// queryInt parses an optional int query param; 0 when absent/invalid.
func queryInt(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}
