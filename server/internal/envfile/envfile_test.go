package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# comment
OPENAI_API_KEY=sk-secret123

export OPENAI_MODEL=deepseek-v4-flash
QUOTED="quoted value"
SINGLE='single value'
SPACED = spaced
TRAILING=value # inline comment
EMPTY=
NOEQUALS
ALREADY_SET=from-file
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALREADY_SET", "from-env")

	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{
		{"OPENAI_API_KEY", "sk-secret123"},
		{"OPENAI_MODEL", "deepseek-v4-flash"},
		{"QUOTED", "quoted value"},
		{"SINGLE", "single value"},
		{"SPACED", "spaced"},
		{"TRAILING", "value"},
		{"EMPTY", ""},
		{"ALREADY_SET", "from-env"}, // real env wins
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
	if _, ok := os.LookupEnv("NOEQUALS"); ok {
		t.Error("NOEQUALS should not be set")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing file returned %v, want nil", err)
	}
}

// An env file the process cannot read (production: /etc/dhtsearch/
// dhtsearch.env is 0600 root-owned while the server runs unprivileged,
// with systemd's EnvironmentFile having already applied the values) is
// not an error either: the file is a best-effort convenience layer and
// the real environment is authoritative. When the tests run as root the
// open succeeds despite mode 0000, so this only bites unprivileged —
// run the compiled test binary as an unprivileged user to see it fail
// against a loader that surfaces EACCES.
func TestLoadUnreadableFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("FROM_UNREADABLE_FILE=1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := Load(path); err != nil {
		t.Fatalf("unreadable file returned %v, want nil", err)
	}
}
