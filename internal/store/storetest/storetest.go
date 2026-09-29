// Package storetest picks the database for tests: a temporary SQLite file, or a fresh
// PostgreSQL schema when LANSCAPE_TEST_POSTGRES holds a postgres:// URL.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
)

// DSN returns a database for one test; PostgreSQL schemas are dropped when the test ends.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv("LANSCAPE_TEST_POSTGRES")
	if base == "" {
		return filepath.Join(t.TempDir(), "lanscape.db")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		db.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
