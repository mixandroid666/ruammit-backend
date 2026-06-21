-- Initial schema for Waymeet. Covers users, social auth, the follow graph,
-- the feed (posts/images/likes/comments/stories), 1:1 chat, and push devices.

CREATE EXTENSION IF NOT EXISTS postgis;   -- geospatial discovery (Location tab)

-- Users ----------------------------------------------------------------------
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT,                          -- null for social-only accounts
    display_name  TEXT NOT NULL,
    avatar_url    TEXT,
    bio           TEXT,
    gender        TEXT,                           -- 'male' | 'female' | 'other'
    birth_date    DATE,
    location      GEOGRAPHY(Point, 4326),         -- last known location
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- GiST index makes ST_DWithin radius queries fast.
CREATE INDEX idx_users_location ON users USING GIST (location);

-- OAuth identities (google/facebook/tiktok) ----------------------------------
CREATE TABLE social_identities (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider     TEXT NOT NULL,                   -- 'google'|'facebook'|'tiktok'
    provider_uid TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_uid)
);

-- Follow graph ---------------------------------------------------------------
CREATE TABLE follows (
    follower_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    followee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (follower_id, followee_id),
    CHECK (follower_id <> followee_id)
);

-- Posts ----------------------------------------------------------------------
CREATE TABLE posts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_posts_author_created ON posts (author_id, created_at DESC);

-- 0-6 images per post.
CREATE TABLE post_images (
    id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id  UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    url      TEXT NOT NULL,
    position SMALLINT NOT NULL DEFAULT 0
);
CREATE INDEX idx_post_images_post ON post_images (post_id, position);

CREATE TABLE post_likes (
    post_id    UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id)
);

CREATE TABLE comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id    UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_comments_post_created ON comments (post_id, created_at);

-- Stories (ephemeral, 24h) ---------------------------------------------------
CREATE TABLE stories (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    media_url  TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '24 hours'
);
CREATE INDEX idx_stories_expires ON stories (expires_at);

-- Push devices (FCM) ---------------------------------------------------------
CREATE TABLE devices (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fcm_token  TEXT NOT NULL UNIQUE,
    platform   TEXT NOT NULL,                      -- 'ios' | 'android'
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
