-- Demo seed data for the Ruammit feed (travel social app).
--
-- Safe to run repeatedly: every row uses a fixed UUID and the script upserts
-- users / re-inserts posts, so re-running resets the demo content without
-- touching real accounts created through the app.
--
-- Login for any seeded account:  password = "password123"
-- The "You" demo account:        demo@ruammit.app
--
-- Apply with:  make seed     (or)   psql "$DATABASE_URL" -f db/seed/0001_demo_feed.sql

BEGIN;

-- 1. Demo users -------------------------------------------------------------
-- All share the same bcrypt hash of "password123". `demo@ruammit.app` is the
-- account you log in as to see a populated home timeline (it follows the rest).
INSERT INTO users (id, email, password_hash, display_name, avatar_url, bio, gender, birth_date, status, verified_at)
VALUES
  ('00000000-0000-0000-0000-000000000001', 'demo@ruammit.app',  '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'You',          'https://i.pravatar.cc/300?u=ruammit-demo',  'Exploring one city at a time.',          'other',  '1996-04-12', 'active', now()),
  ('00000000-0000-0000-0000-000000000002', 'maya@ruammit.app',  '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Maya Wanders', 'https://i.pravatar.cc/300?u=ruammit-maya',  'Solo traveler · 38 countries and counting.', 'female', '1994-09-02', 'active', now()),
  ('00000000-0000-0000-0000-000000000003', 'kenji@ruammit.app', '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Kenji Tan',    'https://i.pravatar.cc/300?u=ruammit-kenji', 'Street food hunter from Bangkok.',       'male',   '1992-01-25', 'active', now()),
  ('00000000-0000-0000-0000-000000000004', 'aroon@ruammit.app', '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Aroon Pol',    'https://i.pravatar.cc/300?u=ruammit-aroon', 'Mountains > everything. Chiang Mai based.', 'male',  '1990-11-08', 'active', now()),
  ('00000000-0000-0000-0000-000000000005', 'sofia@ruammit.app', '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Sofia Reyes',  'https://i.pravatar.cc/300?u=ruammit-sofia', 'Beaches, boats and sunsets.',            'female', '1997-06-19', 'active', now()),
  ('00000000-0000-0000-0000-000000000006', 'liam@ruammit.app',  '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Liam Ferraro', 'https://i.pravatar.cc/300?u=ruammit-liam',  'Backpacker. Coffee snob. Map nerd.',     'male',   '1993-03-30', 'active', now()),
  ('00000000-0000-0000-0000-000000000007', 'nok@ruammit.app',   '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Nok Chai',     'https://i.pravatar.cc/300?u=ruammit-nok',   'Temples, markets, and slow mornings.',   'female', '1995-12-14', 'active', now()),
  ('00000000-0000-0000-0000-000000000008', 'hana@ruammit.app',  '$2a$10$BBAyh8Ui11KHuJyYjmK8x.y0OK8147nnahaCjBWynP4tt5a8FFufG', 'Hana Sato',    'https://i.pravatar.cc/300?u=ruammit-hana',  'Photographer chasing golden hour.',      'female', '1991-08-21', 'active', now())
ON CONFLICT (id) DO UPDATE SET
  email         = EXCLUDED.email,
  password_hash = EXCLUDED.password_hash,
  display_name  = EXCLUDED.display_name,
  avatar_url    = EXCLUDED.avatar_url,
  bio           = EXCLUDED.bio,
  gender        = EXCLUDED.gender,
  birth_date    = EXCLUDED.birth_date,
  status        = EXCLUDED.status,
  verified_at   = EXCLUDED.verified_at;

-- 2. Follow graph -----------------------------------------------------------
-- The demo account follows everyone (so its home timeline is full), plus a few
-- cross-follows so the graph isn't a pure star.
INSERT INTO follows (follower_id, followee_id)
SELECT '00000000-0000-0000-0000-000000000001', id
FROM users
WHERE id IN (
  '00000000-0000-0000-0000-000000000002',
  '00000000-0000-0000-0000-000000000003',
  '00000000-0000-0000-0000-000000000004',
  '00000000-0000-0000-0000-000000000005',
  '00000000-0000-0000-0000-000000000006',
  '00000000-0000-0000-0000-000000000007',
  '00000000-0000-0000-0000-000000000008'
)
ON CONFLICT DO NOTHING;

INSERT INTO follows (follower_id, followee_id) VALUES
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000003'),
  ('00000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000004'),
  ('00000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000002'),
  ('00000000-0000-0000-0000-000000000006', '00000000-0000-0000-0000-000000000008')
ON CONFLICT DO NOTHING;

-- 3. Posts ------------------------------------------------------------------
-- Reset any prior seed posts first; ON DELETE CASCADE clears their images,
-- likes and comments so counts stay correct on re-run.
DELETE FROM posts WHERE id IN (
  'a0000000-0000-0000-0000-000000000001','a0000000-0000-0000-0000-000000000002',
  'a0000000-0000-0000-0000-000000000003','a0000000-0000-0000-0000-000000000004',
  'a0000000-0000-0000-0000-000000000005','a0000000-0000-0000-0000-000000000006',
  'a0000000-0000-0000-0000-000000000007','a0000000-0000-0000-0000-000000000008',
  'a0000000-0000-0000-0000-000000000009','a0000000-0000-0000-0000-000000000010',
  'a0000000-0000-0000-0000-000000000011','a0000000-0000-0000-0000-000000000012',
  'a0000000-0000-0000-0000-000000000013','a0000000-0000-0000-0000-000000000014'
);

INSERT INTO posts (id, author_id, body, created_at) VALUES
  ('a0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', 'Sunrise over Bagan from a hot air balloon — no photo does it justice. Worth every 4am alarm. 🎈 #myanmar #travel', now() - interval '35 minutes'),
  ('a0000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000003', 'Best plate of pad kra pao I have had all year, from a tiny cart in Chinatown. 60 baht of pure joy.', now() - interval '1 hour 20 minutes'),
  ('a0000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000004', 'Two days trekking in Pai and the fog this morning made it feel like another planet.', now() - interval '2 hours 5 minutes'),
  ('a0000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000005', 'Island hopping around Krabi today. Found a beach with literally nobody else on it.', now() - interval '3 hours 40 minutes'),
  ('a0000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000006', 'Hot take: the best travel days are the unplanned ones. Missed my bus, met three new friends, ended up here. ☕', now() - interval '5 hours'),
  ('a0000000-0000-0000-0000-000000000006', '00000000-0000-0000-0000-000000000007', 'Morning alms round in Luang Prabang. Quiet, humbling, unforgettable.', now() - interval '7 hours 15 minutes'),
  ('a0000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000008', 'Golden hour at Angkor Wat. Showed up at 5am with my tripod and it paid off.', now() - interval '9 hours'),
  ('a0000000-0000-0000-0000-000000000008', '00000000-0000-0000-0000-000000000002', 'Packing list reality check: I used maybe 40% of what I brought. Less is more, every single time.', now() - interval '11 hours'),
  ('a0000000-0000-0000-0000-000000000009', '00000000-0000-0000-0000-000000000003', 'Found a night market that locals actually go to. Grilled squid, mango sticky rice, and zero tourists.', now() - interval '14 hours'),
  ('a0000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000004', 'Doi Inthanon at dawn. Coldest I have ever been in Thailand and I would do it again tomorrow.', now() - interval '18 hours'),
  ('a0000000-0000-0000-0000-000000000011', '00000000-0000-0000-0000-000000000005', 'Longtail boat, calm sea, and a cooler full of fresh fruit. This is the whole itinerary today.', now() - interval '22 hours'),
  ('a0000000-0000-0000-0000-000000000012', '00000000-0000-0000-0000-000000000006', 'Three espressos deep, planning the next leg of the trip on a napkin. The classic method.', now() - interval '1 day 3 hours'),
  ('a0000000-0000-0000-0000-000000000013', '00000000-0000-0000-0000-000000000007', 'Wat Rong Khun (the White Temple) is even more surreal in person. Stayed until they closed the gates.', now() - interval '1 day 9 hours'),
  ('a0000000-0000-0000-0000-000000000014', '00000000-0000-0000-0000-000000000008', 'Street portraits from Hoi An. The lanterns turn the whole old town gold after dark.', now() - interval '2 days');

-- 4. Post media --------------------------------------------------------------
-- Unified images + video in post_media (media_order is 1-based). Posts have
-- 1-4 images so both the single-image and slider states render; post 8 is a
-- video-only post so the feed's video state is exercised too.
INSERT INTO post_media (post_id, media_type, media_url, media_order) VALUES
  ('a0000000-0000-0000-0000-000000000001', 'image', 'https://picsum.photos/seed/ruammit-p1a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000001', 'image', 'https://picsum.photos/seed/ruammit-p1b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000001', 'image', 'https://picsum.photos/seed/ruammit-p1c/800/600', 3),
  ('a0000000-0000-0000-0000-000000000002', 'image', 'https://picsum.photos/seed/ruammit-p2a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000003', 'image', 'https://picsum.photos/seed/ruammit-p3a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000003', 'image', 'https://picsum.photos/seed/ruammit-p3b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000004', 'image', 'https://picsum.photos/seed/ruammit-p4a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000004', 'image', 'https://picsum.photos/seed/ruammit-p4b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000004', 'image', 'https://picsum.photos/seed/ruammit-p4c/800/600', 3),
  ('a0000000-0000-0000-0000-000000000004', 'image', 'https://picsum.photos/seed/ruammit-p4d/800/600', 4),
  ('a0000000-0000-0000-0000-000000000005', 'image', 'https://picsum.photos/seed/ruammit-p5a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000006', 'image', 'https://picsum.photos/seed/ruammit-p6a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000006', 'image', 'https://picsum.photos/seed/ruammit-p6b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000007', 'image', 'https://picsum.photos/seed/ruammit-p7a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000007', 'image', 'https://picsum.photos/seed/ruammit-p7b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000007', 'image', 'https://picsum.photos/seed/ruammit-p7c/800/600', 3),
  ('a0000000-0000-0000-0000-000000000008', 'video', 'https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/ForBiggerBlazes.mp4', 1),
  ('a0000000-0000-0000-0000-000000000009', 'image', 'https://picsum.photos/seed/ruammit-p9a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000010', 'image', 'https://picsum.photos/seed/ruammit-p10a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000010', 'image', 'https://picsum.photos/seed/ruammit-p10b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000011', 'image', 'https://picsum.photos/seed/ruammit-p11a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000011', 'image', 'https://picsum.photos/seed/ruammit-p11b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000013', 'image', 'https://picsum.photos/seed/ruammit-p13a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000014', 'image', 'https://picsum.photos/seed/ruammit-p14a/800/600', 1),
  ('a0000000-0000-0000-0000-000000000014', 'image', 'https://picsum.photos/seed/ruammit-p14b/800/600', 2),
  ('a0000000-0000-0000-0000-000000000014', 'image', 'https://picsum.photos/seed/ruammit-p14c/800/600', 3);

-- 4b. Post locations ----------------------------------------------------------
INSERT INTO post_locations (post_id, latitude, longitude, location_name) VALUES
  ('a0000000-0000-0000-0000-000000000001', 21.1722,  94.8585,  'Bagan, Myanmar'),
  ('a0000000-0000-0000-0000-000000000004',  8.0863,  98.9063,  'Krabi, Thailand'),
  ('a0000000-0000-0000-0000-000000000007', 13.4125, 103.8670,  'Angkor Wat, Cambodia'),
  ('a0000000-0000-0000-0000-000000000010', 18.5885,  98.4867,  'Doi Inthanon, Thailand');

-- 5. Likes ------------------------------------------------------------------
-- Give a handful of popular posts a like from every seeded user (except the
-- author) so like counts vary. Restricted to the seeded demo accounts so real
-- test accounts created through the app are left untouched.
INSERT INTO post_likes (post_id, user_id)
SELECT p.post_id::uuid, u.id
FROM (VALUES
  ('a0000000-0000-0000-0000-000000000001'),
  ('a0000000-0000-0000-0000-000000000002'),
  ('a0000000-0000-0000-0000-000000000004'),
  ('a0000000-0000-0000-0000-000000000007'),
  ('a0000000-0000-0000-0000-000000000010')
) AS p(post_id)
CROSS JOIN users u
WHERE u.id::text LIKE '00000000-0000-0000-0000-%'
  AND u.id <> (SELECT author_id FROM posts WHERE id = p.post_id::uuid)
ON CONFLICT DO NOTHING;

-- Explicit likes by the demo account so the heart shows as filled for these.
INSERT INTO post_likes (post_id, user_id) VALUES
  ('a0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001'),
  ('a0000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001'),
  ('a0000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000001')
ON CONFLICT DO NOTHING;

-- 6. Comments ---------------------------------------------------------------
INSERT INTO comments (post_id, author_id, body, created_at) VALUES
  ('a0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000003', 'This is on my bucket list now. Which company did you fly with?', now() - interval '20 minutes'),
  ('a0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001', 'Unreal shot 🔥', now() - interval '12 minutes'),
  ('a0000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000005', 'Drop the location please!!', now() - interval '50 minutes'),
  ('a0000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000002', 'Saving this for my Krabi trip next month.', now() - interval '3 hours'),
  ('a0000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000004', 'The 5am wake-up is always worth it at Angkor.', now() - interval '8 hours'),
  ('a0000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000001', 'Incredible composition.', now() - interval '7 hours 30 minutes');

-- 7. Stories (active, expire 24h from now) ----------------------------------
DELETE FROM stories WHERE id IN (
  'c0000000-0000-0000-0000-000000000001','c0000000-0000-0000-0000-000000000002',
  'c0000000-0000-0000-0000-000000000003','c0000000-0000-0000-0000-000000000004',
  'c0000000-0000-0000-0000-000000000005','c0000000-0000-0000-0000-000000000006'
);

INSERT INTO stories (id, author_id, media_url, created_at, expires_at) VALUES
  ('c0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', 'https://picsum.photos/seed/ruammit-s1/400/700', now() - interval '40 minutes', now() + interval '23 hours'),
  ('c0000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000003', 'https://picsum.photos/seed/ruammit-s2/400/700', now() - interval '1 hour 30 minutes', now() + interval '22 hours'),
  ('c0000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000005', 'https://picsum.photos/seed/ruammit-s3/400/700', now() - interval '2 hours', now() + interval '22 hours'),
  ('c0000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000006', 'https://picsum.photos/seed/ruammit-s4/400/700', now() - interval '3 hours', now() + interval '21 hours'),
  ('c0000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000007', 'https://picsum.photos/seed/ruammit-s5/400/700', now() - interval '4 hours', now() + interval '20 hours'),
  ('c0000000-0000-0000-0000-000000000006', '00000000-0000-0000-0000-000000000008', 'https://picsum.photos/seed/ruammit-s6/400/700', now() - interval '6 hours', now() + interval '18 hours');

-- 8. Backfill the denormalized counters for the seeded posts ------------------
UPDATE posts p SET
  media_count  = (SELECT count(*) FROM post_media     m WHERE m.post_id = p.id),
  has_location = EXISTS (SELECT 1 FROM post_locations l WHERE l.post_id = p.id)
WHERE p.id::text LIKE 'a0000000-%';

COMMIT;
