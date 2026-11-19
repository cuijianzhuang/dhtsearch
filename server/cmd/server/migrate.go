package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"dhtsearch/server/internal/store"
)

// The migrate subcommand copies the whole database between backends:
//
//	dhtsearch-server migrate --from <src> --to <dst>
//
// Each endpoint is a Postgres URL (postgres:// or postgresql://) or a
// SQLite file path, so both directions work. It exists as a subcommand
// of the server binary — rather than a separate tool — because the
// release package ships exactly one binary, and the installer's
// database switch needs to run it on machines with nothing else
// installed. Copy semantics and verification live in store.Migrate:
// existing primary keys in the target are skipped, never overwritten,
// and every source key is re-checked against the target afterwards.
//
// Stop the service before migrating its live database: rows written
// mid-copy are not picked up. Exit status is 0 only when every table
// copied and verified clean.

// describeEndpoint renders an endpoint for logs and reports. Postgres
// URLs carry a password, so they are masked to scheme, user, host and
// database name; SQLite endpoints are just paths.
func describeEndpoint(ep string) string {
	if !store.IsPostgresURL(ep) {
		return "sqlite:" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return "postgres:(unparseable URL)"
	}
	user := ""
	if u.User != nil {
		user = u.User.Username() + "@"
	}
	return "postgres://" + user + u.Host + u.Path
}

func sameEndpoint(a, b string) bool {
	if store.IsPostgresURL(a) || store.IsPostgresURL(b) {
		return a == b
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	from := fs.String("from", "", "source: Postgres URL or SQLite file path")
	to := fs.String("to", "", "target: Postgres URL or SQLite file path")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: dhtsearch-server migrate --from <src> --to <dst>\n")
		fmt.Fprintf(fs.Output(), "Each endpoint is a postgres:// URL or a SQLite file path; both directions work.\n")
		fmt.Fprintf(fs.Output(), "Rows already present in the target are skipped, never overwritten.\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "migrate: --from and --to are both required")
		fs.Usage()
		return 2
	}
	if sameEndpoint(*from, *to) {
		fmt.Fprintln(os.Stderr, "migrate: --from and --to are the same database; nothing to do")
		return 1
	}
	// A missing SQLite source would otherwise be silently created as
	// an empty database and "successfully" migrate zero rows.
	if !store.IsPostgresURL(*from) {
		if _, err := os.Stat(*from); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: source database file: %v\n", err)
			return 1
		}
	}

	src, err := store.OpenURL(*from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: open source %s: %v\n", describeEndpoint(*from), err)
		return 1
	}
	defer src.Close()
	dst, err := store.OpenURL(*to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: open target %s: %v\n", describeEndpoint(*to), err)
		return 1
	}
	defer dst.Close()

	fmt.Printf("Migrating %s -> %s\n", describeEndpoint(*from), describeEndpoint(*to))
	rep, err := store.Migrate(context.Background(), src, dst)
	for _, t := range rep.Tables {
		fmt.Printf("  %-9s source=%d inserted=%d skipped=%d target=%d missing=%d\n",
			t.Table, t.Source, t.Inserted, t.Skipped, t.Dest, t.Missing)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}
	fmt.Println("Migration complete: all source rows verified present in target.")
	return 0
}

// migrateRequested reports whether argv selects the migrate
// subcommand. It is checked before any env file is loaded or server
// flag is parsed, so a migration run never starts the service.
func migrateRequested() bool {
	return len(os.Args) > 1 && os.Args[1] == "migrate"
}
