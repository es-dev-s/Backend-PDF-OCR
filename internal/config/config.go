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
	HeavyConcurrency   int
	TitleConcurrency   int
	PGMaxConns         int32
	RedisPoolSize      int
	MaxInflightUploads int
	Heartbeat          time.Duration
	ShutdownTimeout    time.Duration
	EngineURL          string
	AdminEmail         string
	AdminPassword      string
	AdminName          string
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
		WorkerN:            capWorkers(int(envInt64("WORKER_CONCURRENCY", 2)), hosted()),
		HeavyConcurrency:   int(envInt64("HEAVY_CONCURRENCY", 0)),
		TitleConcurrency:   int(envInt64("TITLE_CONCURRENCY", 0)),
		PGMaxConns:         int32(envInt64("PG_MAX_CONNS", 8)),
		RedisPoolSize:      int(envInt64("REDIS_POOL_SIZE", 8)),
		MaxInflightUploads: int(envInt64("MAX_INFLIGHT_UPLOADS", 8)),
		Heartbeat:          time.Duration(envInt64("HEARTBEAT_SECONDS", 2)) * time.Second,
		ShutdownTimeout:    time.Duration(envInt64("SHUTDOWN_TIMEOUT_SECONDS", 20)) * time.Second,
		EngineURL:          env("ENGINE_BASE_URL", "http://127.0.0.1:8000"),
		AdminEmail:         env("AUTH_ADMIN_EMAIL", ""),
		AdminPassword:      env("AUTH_ADMIN_PASSWORD", ""),
		AdminName:          env("AUTH_ADMIN_NAME", "Admin"),
	}
	if cfg.WorkerN < 1 {
		cfg.WorkerN = 1
	}
	if cfg.MaxSources < 1 {
		cfg.MaxSources = 4
	}
	if cfg.MaxInflightUploads < 1 {
		cfg.MaxInflightUploads = 8
	}
	if hosted() {
		if cfg.PGMaxConns > 8 {
			cfg.PGMaxConns = 8
		}
		if cfg.RedisPoolSize > 8 {
			cfg.RedisPoolSize = 8
		}
		if cfg.MaxInflightUploads > 8 {
			cfg.MaxInflightUploads = 8
		}
	}
	if cfg.PGMaxConns < 4 {
		cfg.PGMaxConns = 8
	}
	if cfg.Heartbeat < time.Second {
		cfg.Heartbeat = 2 * time.Second
	}
	cfg.HeavyConcurrency = capHeavy(cfg.HeavyConcurrency, cfg.WorkerN, hosted())
	cfg.TitleConcurrency = capTitle(cfg.TitleConcurrency, cfg.WorkerN, hosted())

	r2OK := cfg.R2AccountID != "" && cfg.R2AccessKey != "" && cfg.R2Secret != "" && cfg.R2Bucket != ""
	driver, err := pickStorage(cfg.StorageDriver, r2OK, hosted())
	if err != nil {
		return cfg, err
	}
	cfg.StorageDriver = driver
	if cfg.HTTPAddr == "" {
		return cfg, fmt.Errorf("HTTP_ADDR is required")
	}
	return cfg, nil
}

func pickStorage(driver string, r2OK, hosted bool) (string, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if r2OK {
		return "r2", nil
	}
	if hosted || driver == "r2" || driver == "s3" {
		return "", fmt.Errorf("R2 storage requires R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_BUCKET")
	}
	return "local", nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func hosted() bool {
	return strings.TrimSpace(os.Getenv("PORT")) != ""
}

func capWorkers(n int, hosted bool) int {
	if n < 1 {
		n = 2
	}
	max := 4
	if hosted {
		max = 2
	}
	if n > max {
		return max
	}
	return n
}

// capHeavy bounds fingerprinting, which holds a whole file in memory. Small
// containers keep this low so a burst of uploads cannot exhaust the heap.
func capHeavy(n, workers int, hosted bool) int {
	if n < 1 {
		n = workers / 2
		if n < 2 {
			n = 2
		}
	}
	limit := 8
	if hosted {
		limit = 2
	}
	if n > limit {
		n = limit
	}
	return n
}

// capTitle bounds calls to the extraction engine. These wait on the network
// rather than local memory, so they run wider than fingerprinting and, more
// importantly, never queue behind it.
func capTitle(n, workers int, hosted bool) int {
	if n < 1 {
		n = workers
	}
	if n < 1 {
		n = 1
	}
	limit := 16
	if hosted {
		limit = 4
	}
	if n > limit {
		n = limit
	}
	return n
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
