package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ocr-backend/internal/auth"
	"ocr-backend/internal/blob"
	"ocr-backend/internal/config"
	"ocr-backend/internal/documents"
	"ocr-backend/internal/engine"
	"ocr-backend/internal/httpapi"
	"ocr-backend/internal/memlimit"
	"ocr-backend/internal/notifications"
	"ocr-backend/internal/postgres"
	"ocr-backend/internal/realtime"
	"ocr-backend/internal/redisx"
	"ocr-backend/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	hosted := strings.TrimSpace(os.Getenv("PORT")) != ""
	memlimit.Apply(hosted)
	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	store, err := blob.New(blob.Options{
		Driver:   cfg.StorageDriver,
		LocalDir: cfg.StorageDir,
		R2: blob.R2Options{
			AccountID: cfg.R2AccountID,
			AccessKey: cfg.R2AccessKey,
			Secret:    cfg.R2Secret,
			Bucket:    cfg.R2Bucket,
			Endpoint:  cfg.R2Endpoint,
			Prefix:    cfg.R2Prefix,
		},
	})
	if err != nil {
		log.Error("storage", "err", err)
		os.Exit(1)
	}
	if store.Driver() != "r2" {
		if hosted {
			log.Error("hosted storage must be r2")
			os.Exit(1)
		}
		log.Warn("storage is local disk; uploads will not persist across deploys")
	} else {
		readyCtx, readyCancel := context.WithTimeout(context.Background(), 8*time.Second)
		if err := store.Ready(readyCtx); err != nil {
			readyCancel()
			log.Error("r2 not ready", "err", err)
			os.Exit(1)
		}
		readyCancel()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg := postgres.New(cfg.DatabaseURL, log, cfg.PGMaxConns)
	rdb := redisx.New(cfg.RedisURL, log, cfg.RedisPoolSize)
	go pg.Run(ctx)
	go rdb.Run(ctx)

	hub := realtime.NewHub(rdb, log)
	go hub.ListenRedis(ctx)

	pool := worker.New(cfg.WorkerN, log)
	pool.Start(ctx, cfg.WorkerN)

	eng := engine.New(cfg.EngineURL, log)
	notes := notifications.NewRepo(pg.Handle, hub)
	users := auth.NewRepo(pg.Handle)
	docs := documents.NewService(documents.NewRepo(pg.Handle), store, hub, pool, notes, eng, log, cfg.MaxSources, cfg.MaxUploadBytes,
		documents.Limits{Heavy: cfg.HeavyConcurrency, Title: cfg.TitleConcurrency})
	go docs.RunRecovery(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				users.SweepExpired(ctx)
			}
		}
	}()
	go func() {
		wait := time.NewTicker(time.Second)
		defer wait.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if _, err := pg.Status(); err == nil && pg.Handle() != nil {
				seedCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				if err := users.EnsureAdmin(seedCtx, cfg.AdminEmail, cfg.AdminName, cfg.AdminPassword); err != nil {
					log.Warn("seed admin failed", "err", err)
				} else if strings.TrimSpace(cfg.AdminEmail) != "" {
					log.Info("admin account ready", "email", strings.ToLower(strings.TrimSpace(cfg.AdminEmail)))
				}
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-wait.C:
			}
		}
	}()

	api := httpapi.New(cfg, log, pg, rdb, store, hub, docs, notes, eng, users)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Info("listening",
			"addr", cfg.HTTPAddr,
			"storage", store.Driver(),
			"prefix", cfg.R2Prefix,
			"workers", cfg.WorkerN,
			"memlimit", memlimit.Format(memlimit.Current()),
			"cgroup", memlimit.Format(memlimit.CgroupLimit()),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	if r2, ok := store.(*blob.R2); ok {
		go func() {
			importCtx, importCancel := context.WithTimeout(ctx, 3*time.Minute)
			defer importCancel()
			n, err := r2.ImportDir(importCtx, cfg.StorageDir)
			if err != nil {
				log.Warn("import local files to r2", "err", err)
			} else if n > 0 {
				log.Info("imported local files to r2", "count", n)
			}
		}()
	}

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	pool.Stop(shutdownCtx)
	pg.Close()
	rdb.Close()
}
