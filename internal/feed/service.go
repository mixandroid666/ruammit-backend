package feed

import (
	"bytes"
	"context"
	"fmt"
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

// StoryMedia is one piece of media within a story bubble.
type StoryMedia struct {
	URL  string
	Type string // "image" | "video"
}

// StoryAuthor groups one author's active stories for the story strip.
type StoryAuthor struct {
	AuthorID        string
	AuthorName      string
	AuthorAvatarURL string
	Media           []StoryMedia
	LatestAt        time.Time
}

// Story is a single story row returned after creation.
type Story struct {
	ID        string
	AuthorID  string
	MediaURL  string
	MediaType string
	CreatedAt time.Time
	ExpiresAt time.Time
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

	// Fallback: if the viewer follows no one, their home feed contains only
	// their own posts. Detect that and switch to the global discovery timeline
	// so they see content from everyone, not just themselves.
	hasFollowedContent := false
	for _, p := range posts {
		if p.AuthorID != viewerID {
			hasFollowedContent = true
			break
		}
	}
	if !hasFollowedContent && offset == 0 {
		gRows, err := s.db.Queries.ListGlobalTimeline(ctx, dbgen.ListGlobalTimelineParams{
			ViewerID: viewer,
			Off:      offset,
			Lim:      limit,
		})
		if err != nil {
			return nil, err
		}
		source = SourceGlobal
		posts = make([]Post, 0, len(gRows)) // replace home posts, don't append
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
		author.Media = append(author.Media, StoryMedia{URL: r.MediaUrl, Type: r.MediaType})
	}

	out := make([]StoryAuthor, 0, len(ordered))
	for _, a := range ordered {
		out = append(out, *a)
	}
	return out, nil
}

// UserPosts returns a paged list of posts by the given author, decorated with
// the viewer's like state so the heart icon renders correctly.
func (s *Service) UserPosts(ctx context.Context, authorID, viewerID string, limit, offset int32) (*FeedPage, error) {
	author, err := parseUUID(authorID)
	if err != nil {
		return nil, ErrInvalidPostID
	}
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return nil, ErrInvalidPostID
	}
	limit = clampLimit(limit)

	rows, err := s.db.Queries.ListUserPosts(ctx, dbgen.ListUserPostsParams{
		AuthorID: author,
		ViewerID: viewer,
		Off:      offset,
		Lim:      limit,
	})
	if err != nil {
		return nil, err
	}

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
	return &FeedPage{Posts: posts, Source: SourceHome, NextOffset: nextOffset(offset, limit, len(posts))}, nil
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

// CreateStory stores the media file and inserts a story row with a 24-hour TTL.
func (s *Service) CreateStory(ctx context.Context, authorID, mediaType string, data []byte, ext string) (*Story, error) {
	author, err := parseUUID(authorID)
	if err != nil {
		return nil, err
	}

	storyID, err := newUUID()
	if err != nil {
		return nil, err
	}
	idStr := uuidString(storyID)

	key := "stories/" + idStr + "/media" + ext
	url, err := s.media.Save(key, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("save story media: %w", err)
	}

	row, err := s.db.Queries.CreateStory(ctx, dbgen.CreateStoryParams{
		AuthorID:  author,
		MediaUrl:  url,
		MediaType: mediaType,
	})
	if err != nil {
		_ = s.media.RemoveAll("stories/" + idStr)
		return nil, err
	}

	return &Story{
		ID:        uuidString(row.ID),
		AuthorID:  uuidString(row.AuthorID),
		MediaURL:  row.MediaUrl,
		MediaType: row.MediaType,
		CreatedAt: row.CreatedAt.Time,
		ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

// DeleteStory removes a story only when the caller is its author.
func (s *Service) DeleteStory(ctx context.Context, storyID, authorID string) error {
	story, err := parseUUID(storyID)
	if err != nil {
		return ErrInvalidPostID
	}
	author, err := parseUUID(authorID)
	if err != nil {
		return err
	}
	return s.db.Queries.DeleteOwnStory(ctx, dbgen.DeleteOwnStoryParams{ID: story, AuthorID: author})
}

// --- comments --------------------------------------------------------------

// Comment is a single comment on a post.
type Comment struct {
	ID              string
	PostID          string
	AuthorID        string
	AuthorName      string
	AuthorAvatarURL string
	Body            string
	CreatedAt       time.Time
	LikeCount       int64
	LikedByViewer   bool
}

// Comments returns a page of comments for a post, oldest first.
func (s *Service) Comments(ctx context.Context, postID, viewerID string, limit, offset int32) ([]Comment, error) {
	pid, err := parseUUID(postID)
	if err != nil {
		return nil, err
	}
	viewer, err := parseUUID(viewerID)
	if err != nil {
		return nil, err
	}
	limit = clampLimit(limit)
	rows, err := s.db.Queries.ListComments(ctx, dbgen.ListCommentsParams{
		PostID:   pid,
		ViewerID: viewer,
		Lim:      limit,
		Off:      offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, Comment{
			ID:              uuidString(r.ID),
			PostID:          uuidString(r.PostID),
			AuthorID:        uuidString(r.AuthorID),
			AuthorName:      deref(r.AuthorName),
			AuthorAvatarURL: deref(r.AuthorAvatarUrl),
			Body:            r.Body,
			CreatedAt:       r.CreatedAt.Time,
			LikeCount:       r.LikeCount,
			LikedByViewer:   r.LikedByViewer,
		})
	}
	return out, nil
}

// CreateComment adds a comment on a post and returns the created comment.
func (s *Service) CreateComment(ctx context.Context, postID, authorID, body string) (*Comment, error) {
	if body == "" {
		return nil, fmt.Errorf("body is required")
	}
	pid, err := parseUUID(postID)
	if err != nil {
		return nil, err
	}
	aid, err := parseUUID(authorID)
	if err != nil {
		return nil, err
	}
	row, err := s.db.Queries.CreateComment(ctx, dbgen.CreateCommentParams{
		PostID:   pid,
		AuthorID: aid,
		Body:     body,
	})
	if err != nil {
		return nil, err
	}

	user, _ := s.db.Queries.GetUserByID(ctx, aid)

	return &Comment{
		ID:              uuidString(row.ID),
		PostID:          uuidString(row.PostID),
		AuthorID:        uuidString(row.AuthorID),
		AuthorName:      deref(user.DisplayName),
		AuthorAvatarURL: deref(user.AvatarUrl),
		Body:            row.Body,
		CreatedAt:       row.CreatedAt.Time,
	}, nil
}

// DeleteComment deletes the caller's own comment.
func (s *Service) DeleteComment(ctx context.Context, commentID, authorID string) error {
	cid, err := parseUUID(commentID)
	if err != nil {
		return err
	}
	aid, err := parseUUID(authorID)
	if err != nil {
		return err
	}
	return s.db.Queries.DeleteOwnComment(ctx, dbgen.DeleteOwnCommentParams{ID: cid, AuthorID: aid})
}

// CountComments returns the total number of comments on a post.
func (s *Service) CountComments(ctx context.Context, postID string) (int64, error) {
	pid, err := parseUUID(postID)
	if err != nil {
		return 0, err
	}
	n, err := s.db.Queries.CountComments(ctx, pid)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// LikeComment records a like on a comment (idempotent).
func (s *Service) LikeComment(ctx context.Context, commentID, viewerID string) error {
	cid, err := parseUUID(commentID)
	if err != nil {
		return ErrInvalidPostID
	}
	uid, err := parseUUID(viewerID)
	if err != nil {
		return ErrInvalidPostID
	}
	return s.db.Queries.LikeComment(ctx, dbgen.LikeCommentParams{CommentID: cid, UserID: uid})
}

// UnlikeComment removes a like from a comment (idempotent).
func (s *Service) UnlikeComment(ctx context.Context, commentID, viewerID string) error {
	cid, err := parseUUID(commentID)
	if err != nil {
		return ErrInvalidPostID
	}
	uid, err := parseUUID(viewerID)
	if err != nil {
		return ErrInvalidPostID
	}
	return s.db.Queries.UnlikeComment(ctx, dbgen.UnlikeCommentParams{CommentID: cid, UserID: uid})
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
