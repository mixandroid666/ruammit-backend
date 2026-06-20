package feed

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"ruammit-backend/internal/platform/storage/dbgen"
)

// Limits enforced by the service (the handler enforces byte sizes up front).
const (
	MaxCaptionLen = 2000
	MaxImages     = 8
	MaxVideos     = 3
)

// Domain errors for post creation. The handler maps these to HTTP status codes.
var (
	ErrCaptionTooLong  = errors.New("caption exceeds the maximum length")
	ErrEmptyPost       = errors.New("a post needs a caption or media")
	ErrTooManyImages   = errors.New("too many images")
	ErrTooManyVideos   = errors.New("too many videos")
	ErrImagesAndVideo  = errors.New("a post can have images or one video, not both")
	ErrInvalidLocation = errors.New("invalid location coordinates")
	ErrRateLimited     = errors.New("posting too quickly")
)

// NewMedia is one decoded upload ready to be stored.
type NewMedia struct {
	Type string // "image" | "video"
	Ext  string // file extension including the dot, e.g. ".jpg"
	Data []byte
}

// NewLocation is a validated geotag for a post.
type NewLocation struct {
	Latitude  float64
	Longitude float64
	Name      string
}

// CreatePostInput is the validated, decoded create-post request.
type CreatePostInput struct {
	Caption  string
	Images   []NewMedia
	Videos   []NewMedia
	Location *NewLocation
}

// CreatePost validates the input, stores any media, and persists the post,
// its media rows and optional location atomically. If anything fails after the
// files are written, the stored files are removed so nothing is orphaned.
func (s *Service) CreatePost(ctx context.Context, authorID string, in CreatePostInput) (*Post, error) {
	author, err := parseUUID(authorID)
	if err != nil {
		return nil, err
	}

	caption := strings.TrimSpace(in.Caption)
	if len([]rune(caption)) > MaxCaptionLen {
		return nil, ErrCaptionTooLong
	}
	if len(in.Images) > MaxImages {
		return nil, ErrTooManyImages
	}
	if len(in.Videos) > MaxVideos {
		return nil, ErrTooManyVideos
	}
	hasMedia := len(in.Videos) > 0 || len(in.Images) > 0
	if caption == "" && !hasMedia {
		return nil, ErrEmptyPost
	}
	if in.Location != nil {
		l := in.Location
		if l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 {
			return nil, ErrInvalidLocation
		}
	}

	if !s.postLimiter.allow(authorID) {
		return nil, ErrRateLimited
	}

	postID, err := newUUID()
	if err != nil {
		return nil, err
	}
	idStr := uuidString(postID)
	prefix := "posts/" + idStr

	cleanup := func() {
		if err := s.media.RemoveAll(prefix); err != nil {
			s.log.Error("cleanup post media failed", "err", err, "post", idStr)
		}
	}

	// 1. Store media files; collect the public URLs to persist.
	media, err := s.storeMedia(prefix, in)
	if err != nil {
		cleanup()
		return nil, err
	}

	// 2. Persist post + media + location atomically.
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful commit
	q := s.db.Queries.WithTx(tx)

	row, err := q.CreatePostWithMeta(ctx, dbgen.CreatePostWithMetaParams{
		ID:          postID,
		AuthorID:    author,
		Body:        caption,
		MediaCount:  int16(len(media)),
		HasLocation: in.Location != nil,
	})
	if err != nil {
		cleanup()
		return nil, err
	}

	for i := range media {
		mr, err := q.AddPostMedia(ctx, dbgen.AddPostMediaParams{
			PostID:     postID,
			MediaType:  media[i].Type,
			MediaUrl:   media[i].URL,
			MediaOrder: int16(media[i].Order),
		})
		if err != nil {
			cleanup()
			return nil, err
		}
		media[i].ID = uuidString(mr.ID)
	}

	var loc *PostLocation
	if in.Location != nil {
		var namePtr *string
		if name := strings.TrimSpace(in.Location.Name); name != "" {
			namePtr = &name
		}
		if _, err := q.AddPostLocation(ctx, dbgen.AddPostLocationParams{
			PostID:       postID,
			Latitude:     in.Location.Latitude,
			Longitude:    in.Location.Longitude,
			LocationName: namePtr,
		}); err != nil {
			cleanup()
			return nil, err
		}
		loc = &PostLocation{
			Latitude:  in.Location.Latitude,
			Longitude: in.Location.Longitude,
			Name:      deref(namePtr),
		}
	}

	if err := tx.Commit(ctx); err != nil {
		cleanup()
		return nil, err
	}

	return s.assembleCreatedPost(ctx, idStr, authorID, author, caption, row.CreatedAt.Time, media, loc), nil
}

// storeMedia writes each upload to the media store and returns the resulting
// PostMedia entries (URLs filled, IDs assigned later after the DB insert).
func (s *Service) storeMedia(prefix string, in CreatePostInput) ([]PostMedia, error) {
	media := make([]PostMedia, 0, len(in.Images)+len(in.Videos))
	for i := range in.Images {
		order := i + 1
		key := fmt.Sprintf("%s/image_%d%s", prefix, order, in.Images[i].Ext)
		url, err := s.media.Save(key, bytes.NewReader(in.Images[i].Data))
		if err != nil {
			return nil, fmt.Errorf("save image: %w", err)
		}
		media = append(media, PostMedia{Type: "image", URL: url, Order: order})
	}
	for i := range in.Videos {
		order := len(in.Images) + i + 1
		key := fmt.Sprintf("%s/video_%d%s", prefix, i+1, in.Videos[i].Ext)
		url, err := s.media.Save(key, bytes.NewReader(in.Videos[i].Data))
		if err != nil {
			return nil, fmt.Errorf("save video: %w", err)
		}
		media = append(media, PostMedia{Type: "video", URL: url, Order: order})
	}
	return media, nil
}

// assembleCreatedPost builds the Post returned to the client after creation,
// enriching it with the author's display name/avatar.
func (s *Service) assembleCreatedPost(
	ctx context.Context,
	idStr, authorID string,
	author pgtype.UUID,
	caption string,
	createdAt time.Time,
	media []PostMedia,
	loc *PostLocation,
) *Post {
	authorName, avatar := "", ""
	if prof, err := s.db.Queries.GetUserByID(ctx, author); err == nil {
		authorName = deref(prof.DisplayName)
		avatar = deref(prof.AvatarUrl)
	}

	var imageURLs []string
	videoURL := ""
	for _, m := range media {
		switch m.Type {
		case "image":
			imageURLs = append(imageURLs, m.URL)
		case "video":
			if videoURL == "" {
				videoURL = m.URL
			}
		}
	}

	return &Post{
		ID:              idStr,
		AuthorID:        authorID,
		AuthorName:      authorName,
		AuthorAvatarURL: avatar,
		Body:            caption,
		CreatedAt:       createdAt,
		LikeCount:       0,
		CommentCount:    0,
		LikedByViewer:   false,
		ImageURLs:       imageURLs,
		VideoURL:        videoURL,
		Media:           media,
		Location:        loc,
	}
}

// --- helpers ---------------------------------------------------------------

// newUUID returns a random (v4) UUID as a pgtype.UUID. Generating the id in Go
// lets us name the media storage path before the post row is committed.
func newUUID() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

// rateLimiter enforces a minimum interval between actions per key (user id).
// In-memory and per-process — fine for a single API instance; swap for Redis
// when scaling out.
type rateLimiter struct {
	mu       sync.Mutex
	last     map[string]time.Time
	interval time.Duration
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{last: make(map[string]time.Time), interval: interval}
}

// allow reports whether the key may act now, recording the time when it may.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if t, ok := r.last[key]; ok && now.Sub(t) < r.interval {
		return false
	}
	r.last[key] = now
	return true
}
