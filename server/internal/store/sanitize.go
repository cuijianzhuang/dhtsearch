package store

import (
	"strings"
	"unicode/utf8"
)

// sanitizeUTF8 coerces s to valid UTF-8, replacing each run of invalid
// bytes with U+FFFD.
//
// Torrent names and file paths come straight off the DHT network and
// are arbitrary bytes — nothing guarantees they are valid UTF-8.
// SQLite stores any byte sequence without complaint, but Postgres TEXT
// columns enforce the database encoding and reject invalid sequences
// outright (SQLSTATE 22021). Every store write path that lands
// network-supplied text in a TEXT column passes it through here first,
// so both backends hold the same sanitized value. A name that is
// mostly readable stays readable and searchable; only the undecodable
// bytes collapse into replacement characters.
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
