package store

import (
	"context"
	"fmt"
	"testing"

	"dhtsearch/server/internal/filter"
)

// seedFilesTorrent inserts one torrent with n files in a stable order
// (file-0000, file-0001, ...) and returns its hash.
func seedFilesTorrent(t *testing.T, s *Store, n int) string {
	t.Helper()
	hash := "filespage000000000000000000000000000001"
	tor := Torrent{
		InfoHash:  hash,
		Name:      "Files Page Fixture",
		FileCount: n,
		CreatedAt: 1000,
	}
	for i := 0; i < n; i++ {
		tor.Files = append(tor.Files, filter.File{
			Path: fmt.Sprintf("pack/file-%04d.bin", i),
			Size: int64(1000 + i),
		})
		tor.TotalSize += int64(1000 + i)
	}
	if err := s.Upsert(tor); err != nil {
		t.Fatal(err)
	}
	return hash
}

func checkFilesPage(t *testing.T, s *Store, hash string) {
	t.Helper()
	ctx := context.Background()

	// 250 files in pages of 100: 100 + 100 + 50, order preserved.
	wantLens := []int{100, 100, 50}
	for page, want := range wantLens {
		files, total, totalAll, found, err := s.FilesPage(ctx, hash, page+1, 100, "")
		if totalAll != 250 {
			t.Fatalf("page %d: totalAll = %d, want 250", page+1, totalAll)
		}
		if err != nil {
			t.Fatalf("page %d: %v", page+1, err)
		}
		if !found {
			t.Fatalf("page %d: torrent not found", page+1)
		}
		if total != 250 {
			t.Fatalf("page %d: total = %d, want 250", page+1, total)
		}
		if len(files) != want {
			t.Fatalf("page %d: got %d files, want %d", page+1, len(files), want)
		}
		first := page * 100
		if got, want := files[0].Path, fmt.Sprintf("pack/file-%04d.bin", first); got != want {
			t.Fatalf("page %d: first file = %q, want %q (stored order)", page+1, got, want)
		}
		if got, want := files[len(files)-1].Path, fmt.Sprintf("pack/file-%04d.bin", first+want-1); got != want {
			t.Fatalf("page %d: last file = %q, want %q", page+1, got, want)
		}
	}

	// A page past the end is empty but keeps the real total.
	files, total, _, found, err := s.FilesPage(ctx, hash, 99, 100, "")
	if err != nil || !found {
		t.Fatalf("past-end page: found=%v err=%v", found, err)
	}
	if len(files) != 0 || total != 250 {
		t.Fatalf("past-end page: %d files, total %d; want 0 / 250", len(files), total)
	}

	// Unknown hash: found=false, no error.
	if _, _, _, found, err := s.FilesPage(ctx, "0000000000000000000000000000000000000000", 1, 100, ""); err != nil || found {
		t.Fatalf("unknown hash: found=%v err=%v, want false / nil", found, err)
	}

	// Page size larger than the list returns everything at once.
	files, _, _, _, err = s.FilesPage(ctx, hash, 1, 500, "")
	if err != nil || len(files) != 250 {
		t.Fatalf("oversize page: %d files, err %v; want 250", len(files), err)
	}

	// The q filter narrows before paging: "file-01" matches
	// file-0100..file-0199 (100 entries), case-insensitively, and
	// total counts the filtered set while totalAll stays at 250.
	files, total, totalAll, found, err := s.FilesPage(ctx, hash, 1, 100, "FILE-01")
	if err != nil || !found {
		t.Fatalf("filter: found=%v err=%v", found, err)
	}
	if total != 100 || totalAll != 250 || len(files) != 100 {
		t.Fatalf("filter: total=%d totalAll=%d len=%d, want 100 / 250 / 100", total, totalAll, len(files))
	}
	if got, want := files[0].Path, "pack/file-0100.bin"; got != want {
		t.Fatalf("filter: first = %q, want %q", got, want)
	}
	// A filter with no hits is an empty page, not an error.
	files, total, totalAll, _, err = s.FilesPage(ctx, hash, 1, 100, "no-such-file")
	if err != nil || total != 0 || totalAll != 250 || len(files) != 0 {
		t.Fatalf("filter miss: total=%d totalAll=%d len=%d err=%v, want 0 / 250 / 0 / nil", total, totalAll, len(files), err)
	}
}

func TestFilesPage(t *testing.T) {
	s := openTest(t)
	hash := seedFilesTorrent(t, s, 250)
	checkFilesPage(t, s, hash)
}

func TestFilesPageEmptyTorrent(t *testing.T) {
	s := openTest(t)
	if err := s.Upsert(Torrent{InfoHash: "emptyfiles00000000000000000000000000001", Name: "No Files", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	files, total, _, found, err := s.FilesPage(context.Background(), "emptyfiles00000000000000000000000000001", 1, 100, "")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if total != 0 || len(files) != 0 {
		t.Fatalf("total=%d files=%d, want 0 / 0", total, len(files))
	}
}
