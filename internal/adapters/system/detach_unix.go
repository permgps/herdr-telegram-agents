//go:build !windows

package system

import "syscall"

// detachAttr puts the daemon in its own session so it survives the action
// or startup hook that spawned it and never receives its terminal signals.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
