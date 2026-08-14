package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	RedisURL           string
	StorageDriver      string
	StorageDir         string
	R2AccountID        string
	R2AccessKey        string
	R2Secret           string
	R2Bucket           string
	R2Endpoint         string
	R2Prefix           string
	CORSOrigins        []string
	MaxUploadBytes     int64
	MaxSources         int
	WorkerN            int
	PGMaxConns         int32
	RedisPoolSize      int
	MaxInflightUploads int
	Heartbeat          time.Duration
	ShutdownTimeout    time.Duration
	EngineURL          string
}

func Load() (Config, error) {
	_ = godotenv.Load()
	_ = godotenv.Load(".env")

	cfg := Config{
		HTTPAddr:           listenAddr(),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisURL:           strings.TrimSpace(os.Getenv("REDIS_URL")),
		StorageDriver:      env("STORAGE_DRIVER", "local"),
		StorageDir:         env("STORAGE_DIR", "./data/uploads"),
		R2AccountID:        env("R2_ACCOUNT_ID", ""),
		R2AccessKey:        env("R2_ACCESS_KEY_ID", ""),
		R2Secret:           env("R2_SECRET_ACCESS_KEY", ""),
		R2Bucket:           env("R2_BUCKET", ""),
		R2Endpoint:         env("R2_ENDPOINT", ""),
		R2Prefix:           env("R2_PREFIX", "ocr/v1"),
		CORSOrigins:        splitCSV(env("CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")),
		MaxUploadBytes:     envInt64("MAX_UPLOAD_BYTES", 50<<20),
		MaxSources:         int(envInt64("MAX_SOURCES", 4)),
		WorkerN:            int(envInt64("WORKER_CONCURRENCY", 16)),
		PGMaxConns:         int32(envInt64("PG_MAX_CONNS", 16)),
		RedisPoolSize:      int(envInt64("REDIS_POOL_SIZE", 16)),
		MaxInflightUploads: int(envInt64("MAX_INFLIGHT_UPLOADS", 32)),
		Heartbeat:          time.Duration(envInt64("HEARTBEAT_SECONDS", 2)) * time.Second,
		ShutdownTimeout:    time.Duration(envInt64("SHUTDOWN_TIMEOUT_SECONDS", 20)) * time.Second,
		EngineURL:          env("ENGINE_BASE_URL", "http://127.0.0.1:8000"),
	}
	if cfg.WorkerN < 1 {
		cfg.WorkerN = 1
	}
	if cfg.MaxSources < 1 {
		cfg.MaxSources = 4
	}
	if cfg.MaxInflightUploads < 1 {
		cfg.MaxInflightUploads = 32
	}
	if cfg.PGMaxConns < 4 {
		cfg.PGMaxConns = 8
	}
	if cfg.Heartbeat < time.Second {
		cfg.Heartbeat = 2 * time.Second
	}
	if cfg.StorageDriver == "" {
		cfg.StorageDriver = "local"
	}
	if strings.EqualFold(cfg.StorageDriver, "r2") || strings.EqualFold(cfg.StorageDriver, "s3") {
		cfg.StorageDriver = "r2"
		if cfg.R2AccountID == "" || cfg.R2AccessKey == "" || cfg.R2Secret == "" || cfg.R2Bucket == "" {
			return cfg, fmt.Errorf("R2 storage requires R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_BUCKET")
		}
	}
	if cfg.HTTPAddr == "" {
		return cfg, fmt.Errorf("HTTP_ADDR is required")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func listenAddr() string {
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		return "0.0.0.0:" + port
	}
	return env("HTTP_ADDR", "127.0.0.1:8001")
}

func envInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
