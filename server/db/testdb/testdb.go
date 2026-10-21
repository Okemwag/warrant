// Package testdb gives each test package its own migrated Postgres schema.
package testdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/okemwag/warrant/internal/config"
	"github.com/okemwag/warrant/server/db"
)

// New returns a pool on a fresh schema named test_<name>. It skips the test
// when WARRANT_TEST_DATABASE_URL is not set (run `make db`).
func New(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	loadRepoDotEnv()
	url := os.Getenv("WARRANT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WARRANT_TEST_DATABASE_URL not set; run `make db` and set it in .env")
	}
	ctx := context.Background()
	schema := "test_" + name

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("test database unavailable (%v); run `make db`", err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+ident+" CASCADE; CREATE SCHEMA "+ident); err != nil {
		t.Fatalf("testdb: reset schema: %v", err)
	}
	_ = conn.Close(ctx)

	pool, err := db.Open(ctx, url, schema)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	return pool
}

// loadRepoDotEnv loads .env from the repository root, found via go.mod.
func loadRepoDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			_ = config.LoadDotEnv(filepath.Join(dir, ".env"))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}
