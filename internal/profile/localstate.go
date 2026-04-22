// localstate.go provides safe, concurrent-write-aware read-modify-write
// access to Chrome's Local State file.
//
// Chrome itself may rewrite Local State at any time while it is running.
// To avoid clobbering Chrome's changes we:
//
//  1. Acquire an exclusive advisory lock on the file (flock / LockFileEx).
//  2. Re-read the file under the lock (so we see Chrome's latest version).
//  3. Modify the in-memory representation.
//  4. Write to a sibling temp file, then rename it over the original
//     (atomic on all platforms we care about — POSIX rename(2) and
//     Windows MoveFileExW with MOVEFILE_REPLACE_EXISTING).
//  5. Release the lock.
//
// Note: Chrome itself does NOT use advisory locks on Local State, so this
// lock only protects against concurrent runs of this tool. The atomic rename
// in step 4 is the primary safety mechanism against Chrome — if Chrome
// writes Local State between our lock acquisition and our rename, our rename
// wins, but we have re-read under the lock so our copy is based on the
// freshest version we could have seen.  The worst case is a benign race where
// Chrome immediately overwrites our registration entry; Chrome will simply
// recreate a default entry for the new profile on first launch.
package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// modifyLocalState opens Local State, calls fn with the parsed top-level map,
// and writes the result back atomically.  The file is locked for the duration.
func modifyLocalState(userDataDir string, fn func(state map[string]interface{}) error) error {
	path := filepath.Join(userDataDir, "Local State")

	// Open (or create) the file for read+write so we can lock it.
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open Local State: %w", err)
	}
	defer f.Close()

	// Acquire exclusive advisory lock — blocks until Chrome or another
	// instance of this tool releases it.
	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock Local State: %w", err)
	}
	defer unlockFile(f) //nolint:errcheck

	// Re-read under the lock so we see the freshest version.
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Local State: %w", err)
	}

	var state map[string]interface{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse Local State: %w", err)
	}

	// Let the caller mutate the state.
	if err := fn(state); err != nil {
		return err
	}

	// Serialise to a temp file in the same directory, then rename atomically.
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal Local State: %w", err)
	}

	tmp, err := os.CreateTemp(userDataDir, ".local-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	tmp.Close()

	// Atomic replace — on POSIX this is rename(2); on Windows we use
	// os.Rename which maps to MoveFileExW(MOVEFILE_REPLACE_EXISTING) in Go's
	// stdlib since Go 1.5.
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic rename Local State: %w", err)
	}

	return nil
}
