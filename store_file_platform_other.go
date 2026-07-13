//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows && !zos

package ihtml

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

var errFileStoreLockUnsupported = errors.New("ihtml: cross-process file locking is unsupported")

func checkFileStorePlatform() error {
	return fmt.Errorf("%w on %s", errFileStoreLockUnsupported, runtime.GOOS)
}

func tryLockFileStoreFile(*os.File) (bool, error) {
	return false, checkFileStorePlatform()
}

func unlockFileStoreFile(*os.File) error { return nil }

func replaceFileStoreFile(from, to string) error { return os.Rename(from, to) }

func fileStoreDirSyncUnsupported(error) bool { return false }
