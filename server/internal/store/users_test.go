package store

import (
	"context"
	"testing"
)

func TestAdminUsersAndSessions(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if n, err := s.CountAdminUsers(ctx); err != nil || n != 0 {
		t.Fatalf("count = %d, %v; want 0", n, err)
	}

	alice := &AdminUser{Username: "alice", PasswordHash: "hash-a", Role: RoleAdmin, Source: SourceLocal}
	if err := s.CreateAdminUser(ctx, alice, 100); err != nil {
		t.Fatal(err)
	}
	if alice.ID == "" || alice.CreatedAt != 100 || alice.UpdatedAt != 100 {
		t.Fatalf("created user not stamped: %+v", alice)
	}
	bob := &AdminUser{Username: "bob", PasswordHash: "hash-b", Role: RoleReviewer, Source: SourceLocal}
	if err := s.CreateAdminUser(ctx, bob, 101); err != nil {
		t.Fatal(err)
	}
	gh := &AdminUser{Username: "octocat", Role: RoleAdmin, Source: SourceGitHub, ExternalID: "42"}
	if err := s.CreateAdminUser(ctx, gh, 102); err != nil {
		t.Fatal(err)
	}

	// Duplicate username is a unique violation on both backends.
	dup := &AdminUser{Username: "alice", Role: RoleReviewer, Source: SourceLocal}
	if err := s.CreateAdminUser(ctx, dup, 103); !IsUniqueViolation(err) {
		t.Fatalf("duplicate create err = %v, want unique violation", err)
	}

	if n, _ := s.CountAdminUsers(ctx); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}
	if n, _ := s.CountEnabledAdminUsers(ctx); n != 2 {
		t.Fatalf("enabled admins = %d, want 2", n)
	}

	byName, err := s.AdminUserByUsername(ctx, "alice")
	if err != nil || byName == nil || byName.ID != alice.ID || byName.PasswordHash != "hash-a" {
		t.Fatalf("byUsername = %+v, %v", byName, err)
	}
	if u, err := s.AdminUserByUsername(ctx, "nobody"); err != nil || u != nil {
		t.Fatalf("byUsername missing = %+v, %v; want nil, nil", u, err)
	}
	byExt, err := s.AdminUserByExternal(ctx, SourceGitHub, "42")
	if err != nil || byExt == nil || byExt.Username != "octocat" {
		t.Fatalf("byExternal = %+v, %v", byExt, err)
	}
	list, err := s.ListAdminUsers(ctx)
	if err != nil || len(list) != 3 || list[0].Username != "alice" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Update: demote + disable bob (already reviewer), change hash.
	bob.Role = RoleAdmin
	bob.Disabled = true
	bob.PasswordHash = "hash-b2"
	if err := s.UpdateAdminUser(ctx, bob, 200); err != nil {
		t.Fatal(err)
	}
	got, _ := s.AdminUserByID(ctx, bob.ID)
	if got == nil || got.Role != RoleAdmin || !got.Disabled || got.PasswordHash != "hash-b2" || got.UpdatedAt != 200 {
		t.Fatalf("updated = %+v", got)
	}
	if n, _ := s.CountEnabledAdminUsers(ctx); n != 2 {
		t.Fatalf("enabled admins after disable = %d, want 2 (alice + octocat)", n)
	}

	if err := s.TouchAdminUserLogin(ctx, alice.ID, 300); err != nil {
		t.Fatal(err)
	}
	got, _ = s.AdminUserByID(ctx, alice.ID)
	if got == nil || got.LastLoginAt != 300 {
		t.Fatalf("last login = %+v", got)
	}

	// Sessions: create, read, per-user brooms.
	for _, sess := range []AdminSession{
		{TokenHash: "h1", UserID: alice.ID, Method: "password", CreatedAt: 1, ExpiresAt: 9999},
		{TokenHash: "h2", UserID: alice.ID, Method: "password", CreatedAt: 2, ExpiresAt: 9999},
		{TokenHash: "h3", UserID: bob.ID, Method: "github", CreatedAt: 3, ExpiresAt: 9999},
		{TokenHash: "h4", UserID: "", Method: "token", CreatedAt: 4, ExpiresAt: 9999},
	} {
		if err := s.CreateAdminSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := s.AdminSessionByHash(ctx, "h1")
	if err != nil || sess == nil || sess.UserID != alice.ID || sess.Method != "password" {
		t.Fatalf("session = %+v, %v", sess, err)
	}
	if sess, err := s.AdminSessionByHash(ctx, "nope"); err != nil || sess != nil {
		t.Fatalf("missing session = %+v, %v; want nil, nil", sess, err)
	}
	if err := s.DeleteAdminUserSessionsExcept(ctx, alice.ID, "h1"); err != nil {
		t.Fatal(err)
	}
	if sess, _ := s.AdminSessionByHash(ctx, "h2"); sess != nil {
		t.Fatal("h2 should have been deleted by the except-broom")
	}
	if sess, _ := s.AdminSessionByHash(ctx, "h1"); sess == nil {
		t.Fatal("h1 should have survived the except-broom")
	}
	if err := s.DeleteAdminUserSessions(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if sess, _ := s.AdminSessionByHash(ctx, "h3"); sess != nil {
		t.Fatal("h3 should have been deleted with bob's sessions")
	}
	if err := s.DeleteAdminSession(ctx, "h4"); err != nil {
		t.Fatal(err)
	}
	if sess, _ := s.AdminSessionByHash(ctx, "h4"); sess != nil {
		t.Fatal("h4 should have been deleted")
	}

	deleted, err := s.DeleteAdminUser(ctx, bob.ID)
	if err != nil || !deleted {
		t.Fatalf("delete bob = %v, %v", deleted, err)
	}
	if deleted, _ := s.DeleteAdminUser(ctx, bob.ID); deleted {
		t.Fatal("second delete should report false")
	}
	if n, _ := s.CountAdminUsers(ctx); n != 2 {
		t.Fatalf("count after delete = %d, want 2", n)
	}
}
