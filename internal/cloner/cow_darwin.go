//go:build darwin

package cloner

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

// cloneDir attempts an APFS copy-on-write clone of src into dst using
// clonefileat(2). On APFS this is near-instantaneous regardless of directory
// size. Falls back to copyDir if clonefileat is unavailable (e.g. HFS+).
func cloneDir(src, dst string) error {
	// clonefileat(2) requires the destination to not exist yet.
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// CLONE_NOOWNERCOPY = 0x0002 - don't copy ownership (we want our own uid)
	const CLONE_NOOWNERCOPY = 0x0002
	// SYS_clonefileat = 462 on macOS arm64/amd64
	const SYS_clonefileat = 462

	srcFd, err := os.Open(filepath.Dir(src))
	if err != nil {
		return copyDir(src, dst)
	}
	defer srcFd.Close()

	dstFd, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return copyDir(src, dst)
	}
	defer dstFd.Close()

	srcName, err := unix.BytePtrFromString(filepath.Base(src))
	if err != nil {
		return copyDir(src, dst)
	}
	dstName, err := unix.BytePtrFromString(filepath.Base(dst))
	if err != nil {
		return copyDir(src, dst)
	}

	_, _, errno := unix.Syscall6(
		SYS_clonefileat,
		uintptr(srcFd.Fd()),
		uintptr(unsafe.Pointer(srcName)),
		uintptr(dstFd.Fd()),
		uintptr(unsafe.Pointer(dstName)),
		CLONE_NOOWNERCOPY,
		0,
	)
	if errno == 0 {
		return nil
	}

	// ENOTSUP = filesystem doesn't support clonefileat (e.g. HFS+, network fs)
	// EXDEV   = cross-device clone not supported
	if errno == unix.ENOTSUP || errno == unix.EXDEV || errno == unix.ENOSYS {
		return copyDir(src, dst)
	}
	return fmt.Errorf("clonefileat %s → %s: %w", src, dst, errno)
}

// removeVerifiedContents walks dst and removes any verified_contents.json
// files found inside _metadata/ directories.
