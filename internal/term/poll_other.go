//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !zos

package term

import (
	"errors"
	"os"
	"time"
)

var errPTYPollUnsupported = errors.New("pty polling unsupported on this platform")

// pollPTYReadable is compile-safe and deliberately nonblocking on platforms
// where creack/pty Start is already unsupported. Returning an error prevents a
// hypothetical call from falling through to a blocking file.Read path.
func pollPTYReadable(_ *os.File, _ time.Duration) (bool, error) {
	return false, errPTYPollUnsupported
}
