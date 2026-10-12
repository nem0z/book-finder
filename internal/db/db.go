// Package db connects to Postgres and applies the embedded schema migrations.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultURL matches the credentials in docker-compose.yml.
const DefaultURL = "postgres://bookfinder:bookfinder@localhost:5432/bookfinder?sslmode=disable"

const indexesFile = "migrations/003_indexes.sql"

// Arbitrary key so concurrent migrate runs serialise.
const migrationLockKey = 7_330_221

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect opens a pool and verifies the connection. params are extra session settings
// (e.g. synchronous_commit) applied to every connection.
func Connect(ctx context.Context, url string, params map[string]string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	for k, v := range params {
		cfg.ConnConfig.RuntimeParams[k] = v
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return pool, nil
}

// Migrate applies every embedded migration not yet recorded in schema_migrations,
// in file-name order, each in its own transaction. It returns the versions applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if err := lockMigrations(ctx, conn); err != nil {
		return nil, err
	}
	defer unlockMigrations(conn)

	if err := createMigrationsTable(ctx, conn); err != nil {
		return nil, err
	}

	files, err := migrationFiles()
	if err != nil {
		return nil, err
	}

	var applied []string
	for _, file := range files {
		version, ok, err := applyMigration(ctx, conn, file)
		if err != nil {
			return applied, err
		}

		if ok {
			applied = append(applied, version)
		}
	}

	return applied, nil
}

// lockMigrations takes a session-level advisory lock so concurrent runs apply migrations one at a time.
func lockMigrations(ctx context.Context, conn *pgxpool.Conn) error {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}

	return nil
}

// unlockMigrations releases the advisory lock. It uses a fresh context so it still runs after
// cancellation. If the unlock fails, the connection is closed, which makes the server drop the lock.
func unlockMigrations(conn *pgxpool.Conn) {
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
		slog.Warn("release migration lock; closing connection", "err", err)
		if err := conn.Conn().Close(ctx); err != nil {
			slog.Warn("close connection", "err", err)
		}
	}
}

func createMigrationsTable(ctx context.Context, conn *pgxpool.Conn) error {
	_, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	return nil
}

// migrationFiles lists the embedded migration files in the order they must be applied.
func migrationFiles() ([]string, error) {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}

	sort.Strings(files)

	return files, nil
}

// applyMigration applies one migration file unless it is already recorded.
// It returns the migration version and whether it was applied by this call.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, file string) (string, bool, error) {
	version := strings.TrimSuffix(path.Base(file), ".sql")

	done, err := isApplied(ctx, conn, version)
	if err != nil {
		return version, false, err
	}

	if done {
		return version, false, nil
	}

	body, err := migrationsFS.ReadFile(file)
	if err != nil {
		return version, false, fmt.Errorf("read migration %s: %w", version, err)
	}

	if err := runMigration(ctx, conn, version, string(body)); err != nil {
		return version, false, err
	}

	return version, true, nil
}

func isApplied(ctx context.Context, conn *pgxpool.Conn, version string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}

	return exists, nil
}

// runMigration executes a migration and records it in the same transaction, so a failed
// migration leaves no trace.
func runMigration(ctx context.Context, conn *pgxpool.Conn, version, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", version, err)
	}
	defer rollback(tx, version)

	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply migration %s: %w", version, err)
	}

	if err := recordMigration(ctx, tx, version); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", version, err)
	}

	return nil
}

func recordMigration(ctx context.Context, tx pgx.Tx, version string) error {
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}

	return nil
}

// rollback undoes an unfinished migration transaction; after a commit it is a no-op.
func rollback(tx pgx.Tx, version string) {
	if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.Warn("roll back migration", "version", version, "err", err)
	}
}

// IndexesSQL returns the idempotent secondary-index DDL.
func IndexesSQL() string {
	b, err := migrationsFS.ReadFile(indexesFile)
	if err != nil {
		panic(err)
	}

	return string(b)
}

var indexNameRe = regexp.MustCompile(`(?i)CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+(\w+)`)

// IndexNames returns the names of the secondary indexes defined in IndexesSQL.
func IndexNames() []string {
	var names []string
	for _, m := range indexNameRe.FindAllStringSubmatch(IndexesSQL(), -1) {
		names = append(names, m[1])
	}

	return names
}
