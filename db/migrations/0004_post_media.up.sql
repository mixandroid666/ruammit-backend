-- Post media & location: unify image/video attachments under post_media and
-- attach an optional geolocation to a post. Supersedes post_images as the
-- canonical media table (its rows are migrated below; the feed now reads
-- post_media). post_images is left in place for backwards compatibility.

-- Unified media table: ordered images (1-8) OR a single video per post.
CREATE TABLE post_media (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id     UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    media_type  TEXT NOT NULL CHECK (media_type IN ('image', 'video')),
    media_url   TEXT NOT NULL,
    media_order SMALLINT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (post_id, media_order)
);
CREATE INDEX idx_post_media_post ON post_media (post_id, media_order);

-- One optional location per post.
CREATE TABLE post_locations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id       UUID NOT NULL UNIQUE REFERENCES posts(id) ON DELETE CASCADE,
    latitude      DOUBLE PRECISION NOT NULL CHECK (latitude  BETWEEN -90 AND 90),
    longitude     DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    location_name TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_post_locations_post ON post_locations (post_id);

-- Denormalized flags for cheap feed rendering decisions.
ALTER TABLE posts ADD COLUMN media_count  SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE posts ADD COLUMN has_location BOOLEAN  NOT NULL DEFAULT false;

-- Migrate any existing images into the unified table (1-based ordering).
INSERT INTO post_media (post_id, media_type, media_url, media_order)
SELECT post_id, 'image', url, position + 1
FROM post_images
ON CONFLICT (post_id, media_order) DO NOTHING;

-- Backfill the denormalized counters from whatever now exists.
UPDATE posts p
SET media_count = (SELECT count(*) FROM post_media m WHERE m.post_id = p.id);
