//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package state

type fileLock struct{}

func acquireFileLock(string) (*fileLock, error) { return nil, ErrLockUnsupported }
func (*fileLock) Close() error                  { return nil }
