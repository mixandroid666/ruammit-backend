-- name: CreatePost :one
INSERT INTO posts (author_id, body)
VALUES ($1, $2)
RETURNING id, author_id, body, created_at;

-- name: CreatePostWithMeta :one
-- Create a post with a caller-supplied id (so media files can be written under
-- it before the row is committed) and the denormalized media/location flags.
INSERT INTO posts (id, author_id, body, media_count, has_location)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, author_id, body, created_at;

-- name: AddPostImage :exec
INSERT INTO post_images (post_id, url, position)
VALUES ($1, $2, $3);

-- name: AddPostMedia :one
INSERT INTO post_media (post_id, media_type, media_url, media_order)
VALUES ($1, $2, $3, $4)
RETURNING id, post_id, media_type, media_url, media_order, created_at;

-- name: AddPostLocation :one
INSERT INTO post_locations (post_id, latitude, longitude, location_name)
VALUES ($1, $2, $3, $4)
RETURNING id, post_id, latitude, longitude, location_name, created_at;

-- name: ListHomeTimeline :many
-- Fan-out-on-read timeline: posts by the viewer and everyone they follow.
-- Each row carries its author, denormalized counts, the viewer's like state,
-- image URLs (ordered), an optional video URL, and an optional location.
SELECT
    p.id,
    p.author_id,
    u.display_name AS author_name,
    u.avatar_url   AS author_avatar_url,
    p.body,
    p.created_at,
    (SELECT count(*) FROM post_likes l WHERE l.post_id = p.id)  AS like_count,
    (SELECT count(*) FROM comments c   WHERE c.post_id = p.id)  AS comment_count,
    EXISTS (
        SELECT 1 FROM post_likes l
        WHERE l.post_id = p.id AND l.user_id = sqlc.arg(viewer_id)
    ) AS liked_by_viewer,
    ARRAY(
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'image'
        ORDER BY m.media_order
    )::text[] AS image_urls,
    COALESCE((
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'video'
        ORDER BY m.media_order LIMIT 1
    ), '')::text AS video_url,
    loc.latitude      AS loc_latitude,
    loc.longitude     AS loc_longitude,
    loc.location_name AS loc_name
FROM posts p
JOIN users u ON u.id = p.author_id
LEFT JOIN post_locations loc ON loc.post_id = p.id
WHERE p.author_id = sqlc.arg(viewer_id)
   OR p.author_id IN (
        SELECT followee_id FROM follows WHERE follower_id = sqlc.arg(viewer_id)
   )
ORDER BY p.created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: ListGlobalTimeline :many
-- Discovery timeline: the most recent posts from everyone. Used as a fallback
-- when a viewer follows no one yet, so the home feed is never empty.
SELECT
    p.id,
    p.author_id,
    u.display_name AS author_name,
    u.avatar_url   AS author_avatar_url,
    p.body,
    p.created_at,
    (SELECT count(*) FROM post_likes l WHERE l.post_id = p.id)  AS like_count,
    (SELECT count(*) FROM comments c   WHERE c.post_id = p.id)  AS comment_count,
    EXISTS (
        SELECT 1 FROM post_likes l
        WHERE l.post_id = p.id AND l.user_id = sqlc.arg(viewer_id)
    ) AS liked_by_viewer,
    ARRAY(
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'image'
        ORDER BY m.media_order
    )::text[] AS image_urls,
    COALESCE((
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'video'
        ORDER BY m.media_order LIMIT 1
    ), '')::text AS video_url,
    loc.latitude      AS loc_latitude,
    loc.longitude     AS loc_longitude,
    loc.location_name AS loc_name
FROM posts p
JOIN users u ON u.id = p.author_id
LEFT JOIN post_locations loc ON loc.post_id = p.id
ORDER BY p.created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: ListActiveStories :many
-- Non-expired stories with their author, newest first. The service groups these
-- by author into the story strip shown above the timeline.
SELECT
    s.id,
    s.author_id,
    u.display_name AS author_name,
    u.avatar_url   AS author_avatar_url,
    s.media_url,
    s.media_type,
    s.created_at
FROM stories s
JOIN users u ON u.id = s.author_id
WHERE s.expires_at > now()
ORDER BY s.created_at DESC
LIMIT sqlc.arg(lim);

-- name: CreateStory :one
INSERT INTO stories (author_id, media_url, media_type)
VALUES ($1, $2, $3)
RETURNING id, author_id, media_url, media_type, created_at, expires_at;

-- name: DeleteOwnStory :exec
DELETE FROM stories WHERE id = $1 AND author_id = $2;

-- name: ListComments :many
SELECT
    c.id,
    c.post_id,
    c.author_id,
    u.display_name AS author_name,
    u.avatar_url   AS author_avatar_url,
    c.body,
    c.created_at,
    (SELECT count(*) FROM comment_likes cl WHERE cl.comment_id = c.id) AS like_count,
    EXISTS (
        SELECT 1 FROM comment_likes cl
        WHERE cl.comment_id = c.id AND cl.user_id = sqlc.arg(viewer_id)
    ) AS liked_by_viewer
FROM comments c
JOIN users u ON u.id = c.author_id
WHERE c.post_id = sqlc.arg(post_id)
ORDER BY c.created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CreateComment :one
INSERT INTO comments (post_id, author_id, body)
VALUES ($1, $2, $3)
RETURNING id, post_id, author_id, body, created_at;

-- name: DeleteOwnComment :exec
DELETE FROM comments WHERE id = $1 AND author_id = $2;

-- name: CountComments :one
SELECT count(*) FROM comments WHERE post_id = $1;

-- name: LikeComment :exec
INSERT INTO comment_likes (comment_id, user_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: UnlikeComment :exec
DELETE FROM comment_likes WHERE comment_id = $1 AND user_id = $2;

-- name: ListUserPosts :many
-- Posts by a specific author, with the viewer's like state for heart rendering.
SELECT
    p.id,
    p.author_id,
    u.display_name AS author_name,
    u.avatar_url   AS author_avatar_url,
    p.body,
    p.created_at,
    (SELECT count(*) FROM post_likes l WHERE l.post_id = p.id)  AS like_count,
    (SELECT count(*) FROM comments c   WHERE c.post_id = p.id)  AS comment_count,
    EXISTS (
        SELECT 1 FROM post_likes l
        WHERE l.post_id = p.id AND l.user_id = sqlc.arg(viewer_id)
    ) AS liked_by_viewer,
    ARRAY(
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'image'
        ORDER BY m.media_order
    )::text[] AS image_urls,
    COALESCE((
        SELECT m.media_url FROM post_media m
        WHERE m.post_id = p.id AND m.media_type = 'video'
        ORDER BY m.media_order LIMIT 1
    ), '')::text AS video_url,
    loc.latitude      AS loc_latitude,
    loc.longitude     AS loc_longitude,
    loc.location_name AS loc_name
FROM posts p
JOIN users u ON u.id = p.author_id
LEFT JOIN post_locations loc ON loc.post_id = p.id
WHERE p.author_id = sqlc.arg(author_id)
ORDER BY p.created_at DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: LikePost :exec
INSERT INTO post_likes (post_id, user_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: UnlikePost :exec
DELETE FROM post_likes WHERE post_id = $1 AND user_id = $2;
