package feed

import (
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ruammit-backend/internal/auth"
	"ruammit-backend/internal/platform/httpx"
)

// Domain errors mapped to HTTP responses by the handler.
var ErrInvalidPostID = errors.New("invalid post id")

// Upload limits enforced by the handler.
const (
	maxImageBytes      = 5 << 20  // 5 MB per image
	maxVideoBytes      = 50 << 20 // 50 MB for the single video
	maxMultipartMemory = 16 << 20 // keep up to 16 MB in memory; spill the rest to disk
	// Hard cap on the whole request body, with headroom for field/boundary overhead.
	maxRequestBytes = maxVideoBytes + (MaxImages * maxImageBytes) + (4 << 20)
)

// Handler exposes the feed HTTP endpoints. All routes are authenticated; the
// caller's id (the "viewer") comes from the access token via auth.Middleware.
type Handler struct {
	svc  *Service
	auth *auth.Service
	log  *slog.Logger
}

// NewHandler builds the feed handler. It takes the auth service so it can wrap
// its routes in the shared access-token middleware.
func NewHandler(svc *Service, authSvc *auth.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, auth: authSvc, log: log}
}

// RegisterRoutes mounts the feed routes on the given mux, behind auth.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/feed", h.protected(h.home))
	mux.Handle("GET /api/v1/feed/stories", h.protected(h.stories))
	mux.Handle("POST /api/v1/feed/posts/{id}/like", h.protected(h.like))
	mux.Handle("DELETE /api/v1/feed/posts/{id}/like", h.protected(h.unlike))
	mux.Handle("POST /api/v1/posts/create", h.protected(h.create))
}

func (h *Handler) protected(fn http.HandlerFunc) http.Handler {
	return h.auth.Middleware(fn)
}

// --- response DTOs ---------------------------------------------------------

type authorDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type mediaDTO struct {
	ID    string `json:"id"`
	Type  string `json:"type"` // "image" | "video"
	URL   string `json:"url"`
	Order int    `json:"order"`
}

type locationDTO struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"location_name"`
}

type postDTO struct {
	ID            string       `json:"id"`
	Author        authorDTO    `json:"author"`
	Body          string       `json:"body"`
	CreatedAt     time.Time    `json:"created_at"`
	LikeCount     int64        `json:"like_count"`
	CommentCount  int64        `json:"comment_count"`
	LikedByViewer bool         `json:"liked_by_viewer"`
	Images        []string     `json:"images"`
	VideoURL      string       `json:"video_url,omitempty"`
	Media         []mediaDTO   `json:"media,omitempty"`
	Location      *locationDTO `json:"location"`
}

type feedResponse struct {
	Posts      []postDTO `json:"posts"`
	Source     string    `json:"source"`
	NextOffset *int32    `json:"next_offset"`
}

type storyDTO struct {
	Author    authorDTO `json:"author"`
	MediaURLs []string  `json:"media_urls"`
	LatestAt  time.Time `json:"latest_at"`
}

type storiesResponse struct {
	Stories []storyDTO `json:"stories"`
}

// --- handlers --------------------------------------------------------------

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	viewerID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	limit := queryInt(r, "limit", 0)
	offset := queryInt(r, "offset", 0)

	page, err := h.svc.HomeFeed(r.Context(), viewerID, limit, offset)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	posts := make([]postDTO, 0, len(page.Posts))
	for _, p := range page.Posts {
		posts = append(posts, postDTOOf(p))
	}
	httpx.JSON(w, http.StatusOK, feedResponse{
		Posts:      posts,
		Source:     string(page.Source),
		NextOffset: page.NextOffset,
	})
}

func (h *Handler) stories(w http.ResponseWriter, r *http.Request) {
	authors, err := h.svc.Stories(r.Context())
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	out := make([]storyDTO, 0, len(authors))
	for _, a := range authors {
		out = append(out, storyDTO{
			Author:    authorDTO{ID: a.AuthorID, Name: a.AuthorName, AvatarURL: a.AuthorAvatarURL},
			MediaURLs: a.MediaURLs,
			LatestAt:  a.LatestAt,
		})
	}
	httpx.JSON(w, http.StatusOK, storiesResponse{Stories: out})
}

func (h *Handler) like(w http.ResponseWriter, r *http.Request) {
	h.setLike(w, r, true)
}

func (h *Handler) unlike(w http.ResponseWriter, r *http.Request) {
	h.setLike(w, r, false)
}

func (h *Handler) setLike(w http.ResponseWriter, r *http.Request, liked bool) {
	viewerID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	postID := r.PathValue("id")

	var err error
	if liked {
		err = h.svc.LikePost(r.Context(), viewerID, postID)
	} else {
		err = h.svc.UnlikePost(r.Context(), viewerID, postID)
	}
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// create handles POST /api/v1/posts/create — a multipart/form-data request with
// a caption, optional images[] (up to 8) OR a single video, and an optional
// location (latitude, longitude, location_name).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	viewerID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		// http.MaxBytesReader trips ParseMultipartForm when the body is too big.
		if strings.Contains(err.Error(), "request body too large") {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				"The upload is too large.")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "invalid_request",
			"Expected multipart/form-data.")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	input, ok := h.parseCreateInput(w, r)
	if !ok {
		return
	}

	post, err := h.svc.CreatePost(r.Context(), viewerID, *input)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, postDTOOf(*post))
}

// parseCreateInput reads and validates the multipart fields into a
// CreatePostInput. On any validation failure it writes the error and returns
// ok=false.
func (h *Handler) parseCreateInput(w http.ResponseWriter, r *http.Request) (*CreatePostInput, bool) {
	in := &CreatePostInput{Caption: r.FormValue("caption")}

	imageHeaders := r.MultipartForm.File["images"]
	videoHeaders := r.MultipartForm.File["video"]

	if len(imageHeaders) > MaxImages {
		httpx.Error(w, http.StatusBadRequest, "too_many_images",
			"A post can have at most 8 images.")
		return nil, false
	}
	if len(videoHeaders) > 0 && len(imageHeaders) > 0 {
		httpx.Error(w, http.StatusBadRequest, "images_and_video",
			"A post can have images or one video, not both.")
		return nil, false
	}
	if len(videoHeaders) > 1 {
		httpx.Error(w, http.StatusBadRequest, "too_many_videos",
			"A post can have at most one video.")
		return nil, false
	}

	for _, fh := range imageHeaders {
		data, ok := readUpload(w, fh, maxImageBytes, "An image")
		if !ok {
			return nil, false
		}
		ext, ok := imageExt(data, fh.Filename)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "unsupported_media_type",
				"Images must be JPG, PNG or WebP.")
			return nil, false
		}
		in.Images = append(in.Images, NewMedia{Type: "image", Ext: ext, Data: data})
	}

	if len(videoHeaders) == 1 {
		fh := videoHeaders[0]
		data, ok := readUpload(w, fh, maxVideoBytes, "The video")
		if !ok {
			return nil, false
		}
		ext, ok := videoExt(data, fh.Filename)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "unsupported_media_type",
				"Videos must be MP4 or MOV.")
			return nil, false
		}
		in.Video = &NewMedia{Type: "video", Ext: ext, Data: data}
	}

	loc, ok := parseLocation(w, r)
	if !ok {
		return nil, false
	}
	in.Location = loc

	if len([]rune(strings.TrimSpace(in.Caption))) > MaxCaptionLen {
		httpx.Error(w, http.StatusBadRequest, "caption_too_long",
			"Caption must be 2000 characters or fewer.")
		return nil, false
	}
	if strings.TrimSpace(in.Caption) == "" && len(in.Images) == 0 && in.Video == nil {
		httpx.Error(w, http.StatusBadRequest, "empty_post",
			"Add a caption or some media before posting.")
		return nil, false
	}

	return in, true
}

// readUpload opens a multipart file, enforcing the byte cap, and returns its
// bytes. label is used in the error message (e.g. "An image").
func readUpload(w http.ResponseWriter, fh *multipart.FileHeader, max int64, label string) ([]byte, bool) {
	if fh.Size > max {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			label+" is too large.")
		return nil, false
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Could not read an uploaded file.")
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Could not read an uploaded file.")
		return nil, false
	}
	if int64(len(data)) > max {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			label+" is too large.")
		return nil, false
	}
	return data, true
}

// parseLocation reads the optional latitude/longitude/location_name fields.
// Both coordinates must be present together and within range.
func parseLocation(w http.ResponseWriter, r *http.Request) (*NewLocation, bool) {
	latRaw := strings.TrimSpace(r.FormValue("latitude"))
	lngRaw := strings.TrimSpace(r.FormValue("longitude"))
	name := strings.TrimSpace(r.FormValue("location_name"))

	if latRaw == "" && lngRaw == "" {
		if name != "" {
			// A name with no coordinates is allowed (e.g. a place label).
			return &NewLocation{Name: name}, true
		}
		return nil, true
	}
	if latRaw == "" || lngRaw == "" {
		httpx.Error(w, http.StatusBadRequest, "invalid_location",
			"Both latitude and longitude are required.")
		return nil, false
	}
	lat, errLat := strconv.ParseFloat(latRaw, 64)
	lng, errLng := strconv.ParseFloat(lngRaw, 64)
	if errLat != nil || errLng != nil ||
		lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		httpx.Error(w, http.StatusBadRequest, "invalid_location",
			"The location coordinates are invalid.")
		return nil, false
	}
	return &NewLocation{Latitude: lat, Longitude: lng, Name: name}, true
}

// imageExt returns the canonical extension for an allowed image type, sniffing
// the bytes and falling back to the filename extension.
func imageExt(data []byte, filename string) (string, bool) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return ".jpg", true
	case ".png":
		return ".png", true
	case ".webp":
		return ".webp", true
	}
	return "", false
}

// videoExt returns the canonical extension for an allowed video type. MOV in
// particular is often sniffed as octet-stream, so the filename is the fallback.
func videoExt(data []byte, filename string) (string, bool) {
	switch http.DetectContentType(data) {
	case "video/mp4":
		return ".mp4", true
	case "video/quicktime":
		return ".mov", true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp4":
		return ".mp4", true
	case ".mov":
		return ".mov", true
	}
	return "", false
}

// --- helpers ---------------------------------------------------------------

func postDTOOf(p Post) postDTO {
	images := p.ImageURLs
	if images == nil {
		images = []string{}
	}

	var media []mediaDTO
	for _, m := range p.Media {
		media = append(media, mediaDTO{ID: m.ID, Type: m.Type, URL: m.URL, Order: m.Order})
	}

	var loc *locationDTO
	if p.Location != nil {
		loc = &locationDTO{
			Latitude:  p.Location.Latitude,
			Longitude: p.Location.Longitude,
			Name:      p.Location.Name,
		}
	}

	return postDTO{
		ID:            p.ID,
		Author:        authorDTO{ID: p.AuthorID, Name: p.AuthorName, AvatarURL: p.AuthorAvatarURL},
		Body:          p.Body,
		CreatedAt:     p.CreatedAt,
		LikeCount:     p.LikeCount,
		CommentCount:  p.CommentCount,
		LikedByViewer: p.LikedByViewer,
		Images:        images,
		VideoURL:      p.VideoURL,
		Media:         media,
		Location:      loc,
	}
}

// queryInt reads an int32 query parameter, returning def when absent or invalid.
func queryInt(r *http.Request, key string, def int32) int32 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return int32(n)
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidPostID):
		httpx.Error(w, http.StatusBadRequest, "invalid_post_id", "The post id is not valid.")
	case errors.Is(err, ErrCaptionTooLong):
		httpx.Error(w, http.StatusBadRequest, "caption_too_long", "Caption must be 2000 characters or fewer.")
	case errors.Is(err, ErrEmptyPost):
		httpx.Error(w, http.StatusBadRequest, "empty_post", "Add a caption or some media before posting.")
	case errors.Is(err, ErrTooManyImages):
		httpx.Error(w, http.StatusBadRequest, "too_many_images", "A post can have at most 8 images.")
	case errors.Is(err, ErrImagesAndVideo):
		httpx.Error(w, http.StatusBadRequest, "images_and_video", "A post can have images or one video, not both.")
	case errors.Is(err, ErrInvalidLocation):
		httpx.Error(w, http.StatusBadRequest, "invalid_location", "The location coordinates are invalid.")
	case errors.Is(err, ErrRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "You're posting too quickly. Please wait a moment.")
	default:
		h.log.Error("feed service error", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal_error", "")
	}
}
