package store

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err == nil {
		var provider *goose.Provider
		sub, subErr := fs.Sub(migrations, "migrations")
		if subErr != nil {
			db.Close()
			return nil, subErr
		}
		provider, err = goose.NewProvider(goose.DialectSQLite3, db, sub)
		if err == nil {
			_, err = provider.Up(ctx)
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
