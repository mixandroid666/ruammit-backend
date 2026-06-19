// Package config loads runtime configuration from environment variables.
//
// Kept dependency-free on purpose; swap to a richer loader (e.g. caarlos0/env
// or viper) if validation/struct-tag parsing becomes worthwhile.
package config

import (
	"os"
	"time"
)

// Config holds all runtime settings for the API and worker.
type Config struct {
	Env         string // development | staging | production
	HTTPAddr    string // listen address, e.g. ":8080"
	DatabaseURL string
	RedisURL    string

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string

	// Local media storage (used until S3/MinIO upload is wired). Uploaded post
	// images/videos are written under UploadDir and served at MediaURLPrefix.
	UploadDir      string
	MediaURLPrefix string

	FCMCredentialsFile string

	// Email OTP delivery (Resend). When ResendAPIKey is empty, the app falls
	// back to logging OTP codes instead of emailing them (dev default).
	ResendAPIKey string
	OTPEmailFrom string
}

// Load reads configuration from the environment, applying sane defaults so the
// server runs out of the box in local development.
func Load() Config {
	return Config{
		Env:         env("ENV", "development"),
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://ruammit:ruammit@localhost:5432/ruammit?sslmode=disable"),
		RedisURL:    env("REDIS_URL", "redis://localhost:6379/0"),

		JWTSecret:       env("JWT_SECRET", "change-me-in-production"),
		AccessTokenTTL:  envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL: envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),

		S3Endpoint:  env("S3_ENDPOINT", "http://localhost:9000"),
		S3Region:    env("S3_REGION", "us-east-1"),
		S3Bucket:    env("S3_BUCKET", "ruammit-media"),
		S3AccessKey: env("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey: env("S3_SECRET_KEY", "minioadmin"),

		UploadDir:      env("UPLOAD_DIR", "./uploads"),
		MediaURLPrefix: env("MEDIA_URL_PREFIX", "/media"),

		FCMCredentialsFile: env("FCM_CREDENTIALS_FILE", ""),

		ResendAPIKey: env("RESEND_API_KEY", ""),
		// onboarding@resend.dev is Resend's shared sandbox sender, usable
		// without a verified domain. Replace with noreply@<your-domain> once
		// you verify a domain in the Resend dashboard.
		OTPEmailFrom: env("OTP_EMAIL_FROM", "onboarding@resend.dev"),
	}
}

// IsProduction reports whether the app is running in the production environment.
func (c Config) IsProduction() bool { return c.Env == "production" }

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
