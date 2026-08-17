package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// scratchPool opens a pool pinned to a throwaway schema so the migration chain
// can be applied to an empty database without touching live tables.
func scratchPool(t *testing.T, schema string) *pgxpool.Pool {
	t.Helper()
	_ = godotenv.Load("../../.env")
	url := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Skipf("postgres unreachable: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema)); err != nil {
		t.Fatalf("drop scratch schema: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, schema)); err != nil {
		t.Fatalf("create scratch schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("scratch pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanCancel()
		drop, err := pgxpool.New(cleanCtx, url)
		if err != nil {
			return
		}
		defer drop.Close()
		_, _ = drop.Exec(cleanCtx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
	})
	return pool
}

func TestMigrateFromEmptyDatabaseAndRerun(t *testing.T) {
	pool := scratchPool(t, "ocr_migrate_test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("first migrate on empty database: %v", err)
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied == 0 {
		t.Fatal("no migrations recorded")
	}

	// A redeploy replays migrate against the same database; it must be a no-op.
	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var again int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&again); err != nil {
		t.Fatalf("recount migrations: %v", err)
	}
	if again != applied {
		t.Fatalf("migrations re-applied: %d then %d", applied, again)
	}
}

func TestMigratedSchemaHasColumnsTheCodeReads(t *testing.T) {
	pool := scratchPool(t, "ocr_schema_test")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	want := map[string][]string{
		"documents": {"id", "client", "erp", "anzsco", "team", "member", "status",
			"created_at", "owner_id", "review_note", "review_requested_at", "notified_at"},
		"sources": {"id", "document_id", "title", "storage_key", "content_type",
			"size_bytes", "content_sha256", "uniqueness", "score", "created_at",
			"needs_title", "note", "released"},
		"duplicate_matches": {"id", "source_id", "matched_source_id", "title", "erp",
			"score", "uploaded_at", "kind"},
	}
	for table, columns := range want {
		for _, column := range columns {
			var exists bool
			err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = current_schema()
					  AND table_name = $1 AND column_name = $2
				)`, table, column).Scan(&exists)
			if err != nil {
				t.Fatalf("lookup %s.%s: %v", table, column, err)
			}
			if !exists {
				t.Errorf("missing column %s.%s", table, column)
			}
		}
	}

	// The capped listing depends on this index existing under its own name.
	var hasIndex bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = current_schema()
			  AND indexname = 'documents_created_at_id_idx'
		)`).Scan(&hasIndex); err != nil {
		t.Fatalf("index lookup: %v", err)
	}
	if !hasIndex {
		t.Error("documents_created_at_id_idx was not created")
	}
}
