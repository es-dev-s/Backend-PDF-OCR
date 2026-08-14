package main

import (
	"context"
	"log"
	"os"
	"time"

	"ocr-backend/internal/blob"
	"ocr-backend/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	store, err := blob.NewR2(blob.R2Options{
		AccountID: cfg.R2AccountID,
		AccessKey: cfg.R2AccessKey,
		Secret:    cfg.R2Secret,
		Bucket:    cfg.R2Bucket,
		Endpoint:  cfg.R2Endpoint,
		Prefix:    cfg.R2Prefix,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := store.Ready(ctx); err != nil {
		log.Fatal(err)
	}
	n, err := store.PurgeAll(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("r2 empty: removed %d objects from %s", n, cfg.R2Bucket)
	os.Exit(0)
}
