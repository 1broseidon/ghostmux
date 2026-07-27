//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package term

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// pollPTYReadable keeps the PTY descriptor single-owner while bounding how
// long readOutput waits before checking its cancellation channel again.
func pollPTYReadable(file *os.File, timeout time.Duration) (bool, error) {
	poll := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	for {
		ready, err := unix.Poll(poll, int(timeout/time.Millisecond))
		if err == unix.EINTR {
			continue
		}
		return ready > 0, err
	}
}
