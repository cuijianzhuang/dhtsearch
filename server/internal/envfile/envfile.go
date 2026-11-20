// Package envfile loads KEY=VALUE pairs from a .env file into the process
// environment. Real environment variables always win, so a .env file is a
// convenience for local runs and never overrides deployment config.
package envfile

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// Load reads path and sets any variable not already present in the
// environment. A missing file is not an error, and neither is an
// unreadable one: this layer is best-effort convenience for local runs,
// and the real environment is authoritative. Production makes that
// concrete — /etc/dhtsearch/dhtsearch.env is 0600 root-owned, the
// server runs unprivileged, and systemd's EnvironmentFile has already
// applied the values before exec; surfacing EACCES there only spams
// the journal on every start.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseLine(sc.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}

// parseLine splits one .env line. It reports ok=false for blanks and
// comments.
func parseLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, val, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	val = strings.TrimSpace(val)
	// Strip matching quotes; an unquoted value keeps a trailing # comment out.
	if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' ||
		val[0] == '\'' && val[len(val)-1] == '\'') {
		val = val[1 : len(val)-1]
	} else if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return key, val, true
}
