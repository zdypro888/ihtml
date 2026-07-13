//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package ihtml

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func checkFileStorePlatform() error { return nil }

// POSIX record locks are process-scoped on several systems, so FileStore also
// takes the shared in-process gate in store_file.go. F_SETLK is intentionally
// nonblocking; the common loop can then honor context cancellation.
func tryLockFileStoreFile(f *os.File) (bool, error) {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return false, err
}

func unlockFileStoreFile(f *os.File) error {
	lock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 0}
	return unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lock)
}

func replaceFileStoreFile(from, to string) error { return os.Rename(from, to) }

func fileStoreDirSyncUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}
