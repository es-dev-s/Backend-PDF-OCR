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
		HTTPAddr:           env("HTTP_ADDR", "127.0.0.1:8001"),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisURL:           strings.TrimSpace(os.Getenv("REDIS_URL")),
		StorageDriver:      env("STORAGE_DRIVER", "local"),
		StorageDir:         env("STORAGE_DIR", "./data/uploads"),
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
