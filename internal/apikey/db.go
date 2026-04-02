package apikey

import (
	"context"
	"database/sql"
)

// SQLDB abstracts *sql.DB so that both PostgreSQL (via pgx/v5/stdlib) and
// SQLite (via modernc.org/sqlite) can be used interchangeably.
type SQLDB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
