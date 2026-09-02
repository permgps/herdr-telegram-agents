//go:build windows

package system

import "syscall"

// detachedProcess is DETACHED_PROCESS from the Win32 API: no console.
const detachedProcess = 0x00000008

// detachAttr starts the daemon without a console and in a new process
// group so Ctrl-C in the parent never reaches it.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}
