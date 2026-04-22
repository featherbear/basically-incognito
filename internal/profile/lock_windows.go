//go:build windows

package profile

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	// LockFileEx with LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY=0
	// (blocking exclusive lock).
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, // flags — blocking exclusive
		0,                               // reserved
		1, 0,                            // nNumberOfBytesToLockLow / High
		&ol,
	)
}

func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		1, 0,
		&ol,
	)
}
