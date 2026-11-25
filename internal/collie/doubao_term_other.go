//go:build !linux && !darwin

package collie

import "os"

func makeRawInput(f *os.File) (restore func(), ok bool) {
	return func() {}, false
}
