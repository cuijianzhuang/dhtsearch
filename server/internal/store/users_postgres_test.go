package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresAdminUsers exercises the admin account tables on a
// real Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=<redacted>
func TestPostgresAdminUsers(t *testing.T) {
	url := os.Getenv("DHTSEARCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("DHTSEARCH_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	if !IsPostgresURL(url) {
		t.Fatalf("DHTSEARCH_TEST_POSTGRES_URL is not a postgres URL")
	}
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"admin_sessions", "admin_users"} {
		if _, err := admin.Exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
			t.Fatalf("reset test database: %v", err)
		}
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	u := &AdminUser{Username: "pg-admin", PasswordHash: "h", Role: RoleAdmin, Source: SourceLocal}
	if err := s.CreateAdminUser(ctx, u, 1); err != nil {
		t.Fatal(err)
	}
	dup := &AdminUser{Username: "pg-admin", Role: RoleReviewer, Source: SourceLocal}
	if err := s.CreateAdminUser(ctx, dup, 2); !IsUniqueViolation(err) {
		t.Fatalf("duplicate create err = %v, want unique violation", err)
	}
	if err := s.CreateAdminSession(ctx, AdminSession{TokenHash: "ph", UserID: u.ID, Method: "password", CreatedAt: 1, ExpiresAt: 9999}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.AdminSessionByHash(ctx, "ph")
	if err != nil || sess == nil || sess.UserID != u.ID {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if n, _ := s.CountEnabledAdminUsers(ctx); n != 1 {
		t.Fatalf("enabled admins = %d, want 1", n)
	}
}
