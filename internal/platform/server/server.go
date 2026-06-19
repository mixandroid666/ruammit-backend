// Package server wires configuration, logging and routes into an http.Server.
//
// Routing uses the stdlib http.ServeMux with Go 1.22+ method-aware patterns
// (e.g. "GET /api/v1/healthz"). Swap to chi if/when richer routing — route
// groups, URL params, per-group middleware — is needed; the handler signatures
// stay the same.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"ruammit-backend/internal/auth"
	"ruammit-backend/internal/feed"
	"ruammit-backend/internal/platform/config"
	"ruammit-backend/internal/platform/httpx"
	"ruammit-backend/internal/platform/mediastore"
	"ruammit-backend/internal/platform/storage"
	"ruammit-backend/internal/user"
)

// New builds the configured *http.Server, ready to ListenAndServe.
func New(cfg config.Config, log *slog.Logger, db *storage.DB) *http.Server {
	mux := http.NewServeMux()
	registerRoutes(mux, cfg, log, db)

	handler := httpx.Chain(mux,
		httpx.Recoverer(log),
		httpx.Logger(log),
	)

	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// registerRoutes mounts the route tree. As modules land, each gets a
// RegisterRoutes(mux, deps) call here (auth, feed, location, chat, ...).
func registerRoutes(mux *http.ServeMux, cfg config.Config, log *slog.Logger, db *storage.DB) {
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /api/v1/healthz", health)
	mux.HandleFunc("GET /readyz", readiness(db))

	// Auth: registration + OTP verification.
	authSvc := auth.NewService(db, cfg, log, otpSender(cfg, log))
	auth.NewHandler(authSvc, log).RegisterRoutes(mux)

	// Local media storage + a static route to serve uploaded post media.
	mediaStore := mediastore.NewLocal(cfg.UploadDir, cfg.MediaURLPrefix)
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

	// TODO: mount remaining feature modules, e.g.
	//   location.RegisterRoutes(mux, locationService)
	//   chat.RegisterRoutes(mux, chatHub)
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

// health is a liveness probe — the process is up.
func health(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readiness is a readiness probe — dependencies (the database) are reachable.
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
