package metadata

import (
	"github.com/anacrolix/torrent/metainfo"

	"dhtsearch/server/internal/filter"
)

// RecordFromInfo extracts a Record from a parsed info dict. Both fetch
// foundations build their Records through here so a torrent produces
// the identical Record no matter which one fetched it: same name
// (BestName), same total size, FileCount counting every file while
// Files is capped at maxFiles entries.
func RecordFromInfo(info *metainfo.Info, hexHash string, maxFiles int) Record {
	rec := Record{InfoHash: hexHash}
	rec.Name = info.BestName()
	rec.TotalSize = info.TotalLength()
	files := info.UpvertedFiles()
	rec.FileCount = len(files)
	if n := len(files); n > maxFiles {
		files = files[:maxFiles]
	}
	for _, fi := range files {
		rec.Files = append(rec.Files, filter.File{
			Path: fi.DisplayPath(info),
			Size: fi.Length,
		})
	}
	return rec
}
