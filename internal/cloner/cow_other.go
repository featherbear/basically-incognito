//go:build !darwin

package cloner

// cloneDir on non-Darwin platforms falls back to a regular recursive copy.
// On Linux with btrfs/xfs, copy_file_range provides block-level dedup for
// individual files inside copyDir, which is a reasonable approximation.
func cloneDir(src, dst string) error {
	return copyDir(src, dst)
}
