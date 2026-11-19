package store

import (
	"testing"

	"dhtsearch/server/internal/filter"
)

func TestCategoryOf(t *testing.T) {
	cases := []struct {
		name  string
		files []filter.File
		tname string
		want  string
	}{
		{"single video file", []filter.File{{Path: "movie.mkv", Size: 10}}, "Movie", CategoryVideo},
		{"uppercase extension", []filter.File{{Path: "MOVIE.MP4", Size: 10}}, "Movie", CategoryVideo},
		{"largest file wins over first", []filter.File{
			{Path: "cover.jpg", Size: 1}, {Path: "film.mkv", Size: 100},
		}, "Film", CategoryVideo},
		{"largest is archive despite video name", []filter.File{
			{Path: "release.mkv", Size: 1}, {Path: "release.zip", Size: 100},
		}, "Release", CategoryArchive},
		{"audio", []filter.File{{Path: "album/song.flac", Size: 5}}, "Album", CategoryAudio},
		{"image", []filter.File{{Path: "photos/img_01.png", Size: 5}}, "Photos", CategoryImage},
		{"doc", []filter.File{{Path: "book.epub", Size: 5}}, "Book", CategoryDoc},
		{"software iso", []filter.File{{Path: "ubuntu.iso", Size: 5}}, "Ubuntu", CategorySoftware},
		{"unknown ext falls back to name", []filter.File{{Path: "data.xyz", Size: 5}}, "Show.mkv", CategoryVideo},
		{"no files falls back to name", nil, "ubuntu-24.04.iso", CategorySoftware},
		{"no files no ext is other", nil, "Some Release 2024", CategoryOther},
		{"dotfile has no extension", []filter.File{{Path: ".gitignore", Size: 5}}, "repo", CategoryOther},
		{"trailing dot has no extension", []filter.File{{Path: "file.", Size: 5}}, "x", CategoryOther},
		{"nested path ext", []filter.File{{Path: "a/b/c/song.MP3", Size: 5}}, "x", CategoryAudio},
	}
	for _, c := range cases {
		if got := CategoryOf(c.files, c.tname); got != c.want {
			t.Errorf("%s: CategoryOf = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestValidCategory(t *testing.T) {
	for _, k := range CategoryKeys {
		if !ValidCategory(k) {
			t.Errorf("%s should be valid", k)
		}
	}
	for _, bad := range []string{"", "movies", "VIDEO", "all"} {
		if ValidCategory(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
