//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package state

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	fileLockTimeout = 250 * time.Millisecond
	fileLockRetry   = 10 * time.Millisecond
)

type fileLock struct {
	file *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(fileLockTimeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &fileLock{file: file}, nil
		}
		if !errors.Is(err, unix.EINTR) &&
			!errors.Is(err, unix.EWOULDBLOCK) &&
			!errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = file.Close()
			return nil, fmt.Errorf("%w: %q remained busy for %s", ErrLockTimeout, path, fileLockTimeout)
		}
		if remaining < fileLockRetry {
			time.Sleep(remaining)
		} else {
			time.Sleep(fileLockRetry)
		}
	}
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
