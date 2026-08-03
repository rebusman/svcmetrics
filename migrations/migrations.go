// Package migrations embeds the SQL schema migrations and applies them with goose.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// embedFS holds the SQL migration files compiled into the binary.
//
//go:embed *.sql
var embedFS embed.FS

// Up applies every pending migration to db. It is safe to call on an
// already-migrated database.
func Up(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(embedFS)
	if err := goose.SetDialect(string(goose.DialectPostgres)); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
