package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSettingsRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadSettings(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh store: got %v, err %v", got, err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"META_TIMEOUT": "30s", "THEME_COLOR": "violet"}, nil, 111); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"META_TIMEOUT": "60s"}, []string{"THEME_COLOR"}, 222); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: values must have persisted, the deleted key must be gone.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["META_TIMEOUT"] != "60s" {
		t.Fatalf("after reopen: %v, want only META_TIMEOUT=60s", got)
	}
}
