// Package sqlitecopy provides a safe, hot-copy of a SQLite database file using
// the "VACUUM INTO" statement, which produces a consistent, fully checkpointed
// snapshot of the database even while another process (e.g. Chrome) holds a
// write lock on it.
//
// Why VACUUM INTO instead of a raw file copy?
//
//   - A raw file copy of a live SQLite DB can capture the file mid-write,
//     yielding a corrupt or inconsistent copy.
//   - SQLite's WAL mode keeps unflushed pages in a separate -wal file; a raw
//     copy of the main DB without the WAL misses those pages entirely.
//   - VACUUM INTO opens the source DB in read-only mode (so it never blocks or
//     conflicts with Chrome's writer), reads all committed pages through
//     SQLite's own pager, and writes a brand-new, WAL-free, fully checkpointed
//     file at the destination. The result is always consistent.
package sqlitecopy

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// Copy creates a consistent snapshot of the SQLite database at src and writes
// it to dst. dst must not already exist. The parent directory of dst is created
// if needed.
//
// The source database is opened read-only, so this works even when Chrome has
// the file open for writing.
func Copy(src, dst string) error {
	// Ensure the destination directory exists.
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	// Remove a pre-existing destination so VACUUM INTO doesn't error.
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove existing destination: %w", err)
		}
	}

	// Open the source DB read-only and with immutable=1 so SQLite doesn't
	// attempt to acquire any locks at all - safe even when Chrome holds an
	// exclusive lock.
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1&_journal_mode=WAL", src)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open source database %s: %w", src, err)
	}
	defer db.Close()

	// VACUUM INTO writes a clean, fully-merged copy (no WAL, no free-list
	// fragmentation) to the given path. It works at the SQLite pager level and
	// is safe for concurrent readers and writers on the source.
	_, err = db.Exec(fmt.Sprintf(`VACUUM INTO %q`, dst))
	if err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", dst, err)
	}

	return nil
}

// IsSQLite reports whether the file at path looks like a SQLite database by
// checking its 16-byte magic header ("SQLite format 3\000").
// Returns false (not an error) if the file doesn't exist or can't be read.
func IsSQLite(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	header := make([]byte, 16)
	if _, err := f.Read(header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}
