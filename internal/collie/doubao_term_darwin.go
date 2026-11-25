//go:build darwin

package collie

import (
	"os"

	"golang.org/x/sys/unix"
)

// makeRawInput puts fd into non-canonical, no-echo, no-ISIG input mode while
// keeping output post-processing (OPOST/ONLCR) intact so normal "\n" lines
// render cleanly in the PTY.
func makeRawInput(f *os.File) (restore func(), ok bool) {
	fd := int(f.Fd())
	orig, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return func() {}, false
	}
	raw := *orig
	raw.Iflag &^= unix.IXON | unix.ICRNL | unix.INLCR | unix.IGNCR
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw); err != nil {
		return func() {}, false
	}
	saved := *orig
	return func() {
		_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, &saved)
	}, true
}
