//go:build !windows

package system

import "os"

// OpenShared opens path for reading. On Unix a file can be renamed or
// removed while a reader holds it open, so this is a plain os.Open.
func OpenShared(path string) (*os.File, error) {
	return os.Open(path)
}
