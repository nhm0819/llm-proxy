package apikey

import (
	"context"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Dialect identifies which SQL dialect to use for migrations and queries.
const (
	DialectPostgres = "postgres"
	DialectSQLite   = "sqlite"
)

// sqliteMigration is the SQLite-compatible DDL equivalent to the embedded
// PostgreSQL migration. SQLite stores timestamps as TEXT.
const sqliteMigration = `
CREATE TABLE IF NOT EXISTS api_keys (
    key         TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    expires_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys (user_id);
`

// RunMigrations executes the embedded SQL migration files against db.
// For PostgreSQL it runs the embedded .sql files; for SQLite it uses an
// inline DDL that avoids TIMESTAMPTZ and now().
func RunMigrations(ctx context.Context, db SQLDB, dialect string) error {
	if dialect == DialectSQLite {
		if _, err := db.ExecContext(ctx, sqliteMigration); err != nil {
			return fmt.Errorf("apikey: sqlite migration: %w", err)
		}
		return nil
	}

	// PostgreSQL: run embedded migration files in order.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("apikey: read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("apikey: read migration %s: %w", e.Name(), err)
		}
		if _, err := db.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("apikey: exec migration %s: %w", e.Name(), err)
		}
	}
	return nil
}
