package store

import (
	"path"
	"strings"

	"dhtsearch/server/internal/filter"
)

// Categories classify a torrent for the search page's filter tabs. The
// classification is computed at query time from the extension of the
// torrent's main file (its largest file) — deliberately not a stored
// column: the mapping is a presentation concern that changes as new
// formats show up, and a stored value would need a backfill migration
// every time the table below is touched. The cost is that a filtered
// or counted search must look at every match's file list (see
// SearchEx); keyword search already scans every match for its COUNT,
// so the extra work is the file-list decompression, bounded by the
// API's admission gate and search timeout.
const (
	CategoryVideo    = "video"    // 视频
	CategoryAudio    = "audio"    // 音频
	CategoryImage    = "image"    // 图片
	CategoryDoc      = "doc"      // 文档
	CategorySoftware = "software" // 软件
	CategoryArchive  = "archive"  // 压缩包
	CategoryOther    = "other"    // 其他
)

// CategoryKeys lists every category in tab display order.
var CategoryKeys = []string{
	CategoryVideo,
	CategoryAudio,
	CategoryImage,
	CategoryDoc,
	CategorySoftware,
	CategoryArchive,
	CategoryOther,
}

// ValidCategory reports whether c is one of CategoryKeys.
func ValidCategory(c string) bool {
	for _, k := range CategoryKeys {
		if c == k {
			return true
		}
	}
	return false
}

// categoryByExt maps a lower-case file extension (no dot) to a category.
var categoryByExt = map[string]string{
	// 视频
	"mkv": CategoryVideo, "mp4": CategoryVideo, "avi": CategoryVideo,
	"mov": CategoryVideo, "wmv": CategoryVideo, "flv": CategoryVideo,
	"webm": CategoryVideo, "m4v": CategoryVideo, "mpg": CategoryVideo,
	"mpeg": CategoryVideo, "ts": CategoryVideo, "m2ts": CategoryVideo,
	"vob": CategoryVideo, "rm": CategoryVideo, "rmvb": CategoryVideo,
	"3gp": CategoryVideo,
	// 音频
	"mp3": CategoryAudio, "flac": CategoryAudio, "wav": CategoryAudio,
	"aac": CategoryAudio, "ogg": CategoryAudio, "m4a": CategoryAudio,
	"ape": CategoryAudio, "opus": CategoryAudio, "wma": CategoryAudio,
	"aif": CategoryAudio, "aiff": CategoryAudio,
	// 图片
	"jpg": CategoryImage, "jpeg": CategoryImage, "png": CategoryImage,
	"gif": CategoryImage, "webp": CategoryImage, "bmp": CategoryImage,
	"svg": CategoryImage, "avif": CategoryImage, "tif": CategoryImage,
	"tiff": CategoryImage, "ico": CategoryImage, "heic": CategoryImage,
	// 文档
	"pdf": CategoryDoc, "epub": CategoryDoc, "mobi": CategoryDoc,
	"azw": CategoryDoc, "azw3": CategoryDoc, "txt": CategoryDoc,
	"doc": CategoryDoc, "docx": CategoryDoc, "xls": CategoryDoc,
	"xlsx": CategoryDoc, "ppt": CategoryDoc, "pptx": CategoryDoc,
	"chm": CategoryDoc, "djvu": CategoryDoc, "md": CategoryDoc,
	"rtf": CategoryDoc,
	// 软件 (含磁盘镜像)
	"exe": CategorySoftware, "msi": CategorySoftware, "dmg": CategorySoftware,
	"iso": CategorySoftware, "pkg": CategorySoftware, "deb": CategorySoftware,
	"rpm": CategorySoftware, "apk": CategorySoftware, "ipa": CategorySoftware,
	"img": CategorySoftware, "appimage": CategorySoftware, "bin": CategorySoftware,
	// 压缩包
	"zip": CategoryArchive, "rar": CategoryArchive, "7z": CategoryArchive,
	"tar": CategoryArchive, "gz": CategoryArchive, "tgz": CategoryArchive,
	"bz2": CategoryArchive, "xz": CategoryArchive, "zst": CategoryArchive,
}

// extOf returns the lower-case extension of a file path or name, without
// the dot; "" when there is none. A trailing dot or a dotfile segment
// (".gitignore") does not count as an extension.
func extOf(p string) string {
	base := path.Base(p)
	i := strings.LastIndex(base, ".")
	if i <= 0 || i == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[i+1:])
}

// CategoryOf classifies a torrent by the extension of its largest file.
// When the file list is empty (bare infohash rows) or the largest file
// has no recognizable extension, the torrent name's extension gets the
// final say; anything else is CategoryOther.
func CategoryOf(files []filter.File, name string) string {
	if len(files) > 0 {
		main := files[0]
		for _, f := range files[1:] {
			if f.Size > main.Size {
				main = f
			}
		}
		if c, ok := categoryByExt[extOf(main.Path)]; ok {
			return c
		}
	}
	if c, ok := categoryByExt[extOf(name)]; ok {
		return c
	}
	return CategoryOther
}
