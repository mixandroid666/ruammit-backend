// Package server wires configuration, logging and routes into an http.Server.
//
// Routing uses the stdlib http.ServeMux with Go 1.22+ method-aware patterns
// (e.g. "GET /api/v1/healthz"). Swap to chi if/when richer routing â€” route
// groups, URL params, per-group middleware â€” is needed; the handler signatures
// stay the same.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"waymeet-backend/internal/auth"
	"waymeet-backend/internal/chat"
	"waymeet-backend/internal/feed"
	"waymeet-backend/internal/location"
	"waymeet-backend/internal/notification"
	"waymeet-backend/internal/platform/config"
	"waymeet-backend/internal/platform/httpx"
	"waymeet-backend/internal/platform/mediastore"
	"waymeet-backend/internal/platform/storage"
	"waymeet-backend/internal/user"
)

// New builds the configured *http.Server, ready to ListenAndServe.
func New(cfg config.Config, log *slog.Logger, db *storage.DB) *http.Server {
	mux := http.NewServeMux()

	// The chat hub needs a context so it can be stopped on server shutdown.
	hubCtx, hubCancel := context.WithCancel(context.Background())
	registerRoutes(mux, cfg, log, db, hubCtx)

	handler := httpx.Chain(mux,
		httpx.CORS,
		httpx.Recoverer(log),
		httpx.Logger(log),
	)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Cancel the hub when graceful shutdown begins so all WS goroutines exit cleanly.
	srv.RegisterOnShutdown(hubCancel)
	return srv
}

// registerRoutes mounts the route tree. As modules land, each gets a
// RegisterRoutes(mux, deps) call here (auth, feed, location, chat, ...).
func registerRoutes(mux *http.ServeMux, cfg config.Config, log *slog.Logger, db *storage.DB, hubCtx context.Context) {
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /api/v1/healthz", health)
	mux.HandleFunc("GET /readyz", readiness(db))

	// Auth: registration + OTP verification.
	authSvc := auth.NewService(db, cfg, log, otpSender(cfg, log))
	auth.NewHandler(authSvc, log).RegisterRoutes(mux)

	// Media storage: S3/MinIO when credentials are present, local filesystem otherwise.
	// The local file server is always mounted as a fallback for local dev.
	mediaStore := newMediaStore(cfg, log)
	fileServer := http.FileServer(http.Dir(cfg.UploadDir))
	mux.Handle("GET "+cfg.MediaURLPrefix+"/",
		http.StripPrefix(cfg.MediaURLPrefix+"/", fileServer))

	// Feed: home timeline, stories, likes and post creation. Routes are
	// protected by the shared auth middleware (the viewer id comes from the
	// access token).
	feedSvc := feed.NewService(db, mediaStore, log)
	feed.NewHandler(feedSvc, authSvc, log).RegisterRoutes(mux)

	// User: profile read/update (the profile-setup screen after OTP verify).
	userSvc := user.NewService(db, mediaStore, log)
	user.NewHandler(userSvc, authSvc, log).RegisterRoutes(mux)

	// Location: update the viewer's position + nearby-people discovery (PostGIS).
	locationSvc := location.NewService(db, log)
	location.NewHandler(locationSvc, authSvc, log).RegisterRoutes(mux)

	// Notifications: FCM push delivery + device token management.
	notifySvc := notification.New(cfg.FCMCredentialsFile, db, log)
	notification.NewHandler(notifySvc, authSvc, log).RegisterRoutes(mux)

	// Chat: 1v1 real-time messaging over WebSocket.
	chatHub := chat.NewHub(log)
	go chatHub.Run(hubCtx)
	chatSvc := chat.NewService(chatHub, db, notifySvc, log)
	chatSvc.StartPresenceWorker(hubCtx)
	chat.NewHandler(chatHub, chatSvc, authSvc, mediaStore, log).RegisterRoutes(mux)
}

// otpSender chooses how OTP codes are delivered. With a Resend API key set,
// emails go out via Resend (phone contacts fall back to logging, since no SMS
// provider is wired up yet). Without a key, all codes are logged (dev default).
func otpSender(cfg config.Config, log *slog.Logger) auth.Sender {
	logSender := auth.NewLogSender(log)
	if cfg.ResendAPIKey == "" {
		return logSender
	}
	return auth.NewResendSender(cfg.ResendAPIKey, cfg.OTPEmailFrom, log, logSender)
}

// newMediaStore returns an S3Store when S3 credentials are configured, falling
// back to the local filesystem store for dev environments without MinIO running.
func newMediaStore(cfg config.Config, log *slog.Logger) mediastore.Store {
	if cfg.S3Bucket != "" && cfg.S3AccessKey != "" {
		s, err := mediastore.NewS3(cfg.S3Endpoint, cfg.S3Region, cfg.S3Bucket, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3MediaBaseURL)
		if err == nil {
			log.Info("media store: s3", "bucket", cfg.S3Bucket, "endpoint", cfg.S3Endpoint)
			return s
		}
		log.Warn("s3 init failed, falling back to local", "err", err)
	}
	log.Info("media store: local", "dir", cfg.UploadDir)
	return mediastore.NewLocal(cfg.UploadDir, cfg.MediaURLPrefix)
}

// health is a liveness probe â€” the process is up.
func health(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readiness is a readiness probe â€” dependencies (the database) are reachable.
func readiness(db *storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Pool.Ping(ctx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db_unavailable", err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
