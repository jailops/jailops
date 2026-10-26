package testdb

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { Main(m) }

func TestFreshDatabases(t *testing.T) {
	a, b := New(t), New(t)
	if a == b {
		t.Fatal("two tests got the same database")
	}
	ctx := context.Background()
	for _, u := range []string{a, b} {
		c, err := pgx.Connect(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := c.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname='public'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("database is not empty: %d tables, %v", n, err)
		}
		c.Close(ctx)
	}
}
