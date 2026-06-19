package feed

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"ruammit-backend/internal/platform/mediastore"
	"ruammit-backend/internal/platform/storage"
	"ruammit-backend/internal/platform/storage/dbgen"
)

// Tunables for timeline paging.
const (
	defaultLimit = 20
	maxLimit     = 50
	storiesLimit = 60

	// Anti-spam: minimum gap between two posts by the same user.
	postCooldown = 3 * time.Second
)

// Service implements the feed use cases: the home timeline, the story strip,
// likes and post creation. It has no idea HTTP exists — the handler adapts it
// to the web.
type Service struct {
	db    *storage.DB
	media mediastore.Store
	log   *slog.Logger

	postLimiter *rateLimiter
}

// NewService wires the feed service.
func NewService(db *storage.DB, media mediastore.Store, log *slog.Logger) *Service {
	return &Service{
		db:          db,
		media:       media,
		log:         log,
		postLimiter: newRateLimiter(postCooldown),
	}
}

// Post is a single timeline entry, ready for the client.
type Post struct {
	ID              string
	AuthorID        string
	AuthorName      string
	AuthorAvatarURL string
	Body            string
	CreatedAt       time.Time
	LikeCount       int64
	CommentCount    int64
	LikedByViewer   bool
	ImageURLs       []string
	VideoURL        string        // "" when the post has no video
	Media           []PostMedia   // typed media (set on create; nil on timeline)
	Location        *PostLocation // nil when the post has no location
}

// PostMedia is one ordered attachment on a post.
type PostMedia struct {
	ID    string
	Type  string // "image" | "video"
	URL   string
	Order int
}

// PostLocation is a post's optional geotag.
type PostLocation struct {
	Latitude  float64
	Longitude float64
	Name      string
}

// StoryAuthor groups one author's active stories for the story strip.
type StoryAuthor struct {
	AuthorID        string
	AuthorName      string
	AuthorAvatarURL string
	MediaURLs       []string
	LatestAt        time.Time
}

// FeedSource tells the client which timeline it received: the personalized
// "home" feed (viewer + follows) or the "global" discovery fallback used when
// the viewer follows no one yet, so the feed is never empty.
type FeedSource string

const (
	SourceHome   FeedSource = "home"
	SourceGlobal FeedSource = "global"
)

// FeedPage is a page of the timeline plus paging metadata.
type FeedPage struct {
	Posts      []Post
	Source     FeedSource
	NextOffset *int32 // nil when there are no more pages
}

// HomeFeed returns a page of the viewer's home timeline. When the personalized
// timeline is empty on the first page (a brand-new account that follows no one),
// it falls back to a recent global timeline so the screen always has content.
func (s *Service) HomeFeed(ctx context.Context, viewerID string, limit, offset int32) (*FeedPage, error) {
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return nil, err
	}
	limit = clampLimit(limit)

	rows, err := s.db.Queries.ListHomeTimeline(ctx, dbgen.ListHomeTimelineParams{
		ViewerID: viewer,
		Off:      offset,
		Lim:      limit,
	})
	if err != nil {
		return nil, err
	}

	source := SourceHome
	posts := make([]Post, 0, len(rows))
	for _, r := range rows {
		posts = append(posts, Post{
			ID:              uuidString(r.ID),
			AuthorID:        uuidString(r.AuthorID),
			AuthorName:      deref(r.AuthorName),
			AuthorAvatarURL: deref(r.AuthorAvatarUrl),
			Body:            r.Body,
			CreatedAt:       r.CreatedAt.Time,
			LikeCount:       r.LikeCount,
			CommentCount:    r.CommentCount,
			LikedByViewer:   r.LikedByViewer,
			ImageURLs:       r.ImageUrls,
			VideoURL:        r.VideoUrl,
			Location:        locationOf(r.LocLatitude, r.LocLongitude, r.LocName),
		})
	}

	// Fallback: a new user with no follows would otherwise see a blank feed.
	if len(posts) == 0 && offset == 0 {
		gRows, err := s.db.Queries.ListGlobalTimeline(ctx, dbgen.ListGlobalTimelineParams{
			ViewerID: viewer,
			Off:      offset,
			Lim:      limit,
		})
		if err != nil {
			return nil, err
		}
		source = SourceGlobal
		for _, r := range gRows {
			posts = append(posts, Post{
				ID:              uuidString(r.ID),
				AuthorID:        uuidString(r.AuthorID),
				AuthorName:      deref(r.AuthorName),
				AuthorAvatarURL: deref(r.AuthorAvatarUrl),
				Body:            r.Body,
				CreatedAt:       r.CreatedAt.Time,
				LikeCount:       r.LikeCount,
				CommentCount:    r.CommentCount,
				LikedByViewer:   r.LikedByViewer,
				ImageURLs:       r.ImageUrls,
				VideoURL:        r.VideoUrl,
				Location:        locationOf(r.LocLatitude, r.LocLongitude, r.LocName),
			})
		}
	}

	return &FeedPage{Posts: posts, Source: source, NextOffset: nextOffset(offset, limit, len(posts))}, nil
}

// Stories returns active (non-expired) stories grouped by author, newest author
// activity first, so the client can render one bubble per author.
func (s *Service) Stories(ctx context.Context) ([]StoryAuthor, error) {
	rows, err := s.db.Queries.ListActiveStories(ctx, storiesLimit)
	if err != nil {
		return nil, err
	}

	// Rows arrive newest-first; preserve that order for first-seen authors and
	// append each author's media chronologically-newest-first under them.
	byAuthor := make(map[string]*StoryAuthor, len(rows))
	ordered := make([]*StoryAuthor, 0, len(rows))
	for _, r := range rows {
		id := uuidString(r.AuthorID)
		author, ok := byAuthor[id]
		if !ok {
			author = &StoryAuthor{
				AuthorID:        id,
				AuthorName:      deref(r.AuthorName),
				AuthorAvatarURL: deref(r.AuthorAvatarUrl),
				LatestAt:        r.CreatedAt.Time,
			}
			byAuthor[id] = author
			ordered = append(ordered, author)
		}
		author.MediaURLs = append(author.MediaURLs, r.MediaUrl)
	}

	out := make([]StoryAuthor, 0, len(ordered))
	for _, a := range ordered {
		out = append(out, *a)
	}
	return out, nil
}

// LikePost records (idempotently) that the viewer likes a post.
func (s *Service) LikePost(ctx context.Context, viewerID, postID string) error {
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return err
	}
	post, err := parseUUID(postID)
	if err != nil {
		return ErrInvalidPostID
	}
	return s.db.Queries.LikePost(ctx, dbgen.LikePostParams{PostID: post, UserID: viewer})
}

// UnlikePost removes the viewer's like from a post (idempotent).
func (s *Service) UnlikePost(ctx context.Context, viewerID, postID string) error {
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return err
	}
	post, err := parseUUID(postID)
	if err != nil {
		return ErrInvalidPostID
	}
	return s.db.Queries.UnlikePost(ctx, dbgen.UnlikePostParams{PostID: post, UserID: viewer})
}

// --- helpers ---------------------------------------------------------------

func clampLimit(limit int32) int32 {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// nextOffset returns the offset for the following page, or nil when the page
// came back short (the last page).
func nextOffset(offset, limit int32, got int) *int32 {
	if int32(got) < limit {
		return nil
	}
	n := offset + limit
	return &n
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// locationOf builds a *PostLocation from the nullable timeline columns, or nil
// when the post has no location.
func locationOf(lat, lng *float64, name *string) *PostLocation {
	if lat == nil || lng == nil {
		return nil
	}
	return &PostLocation{Latitude: *lat, Longitude: *lng, Name: deref(name)}
}

// parseUUID converts a canonical UUID string into a pgtype.UUID.
func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, ErrInvalidPostID
	}
	return u, nil
}

// uuidString renders a pgtype.UUID in canonical 8-4-4-4-12 form.
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
