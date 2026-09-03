//go:build windows

package system

import (
	"os"
	"syscall"
)

// OpenShared opens path for reading and lets other processes rename or
// delete the file while the handle is open. Go's os.Open omits
// FILE_SHARE_DELETE, so on Windows a reader such as the logs pane would
// otherwise make the daemon's log rotation fail with "the process cannot
// access the file because it is being used by another process".
func OpenShared(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
