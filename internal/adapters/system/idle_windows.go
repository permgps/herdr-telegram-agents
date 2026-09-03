//go:build windows

package system

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	getLastInputInfo = user32.NewProc("GetLastInputInfo")
	getTickCount     = kernel32.NewProc("GetTickCount")
)

// lastInputInfo mirrors LASTINPUTINFO: dwTime is the tick count (ms since
// boot, 32-bit) of the last input event.
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// idleFor subtracts the last input tick from the current tick count; both
// are 32-bit and wrap together, so the unsigned difference stays right.
func idleFor(context.Context) (time.Duration, error) {
	info := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	ok, _, err := getLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, fmt.Errorf("GetLastInputInfo: %w", err)
	}
	now, _, _ := getTickCount.Call()
	return time.Duration(uint32(now)-info.dwTime) * time.Millisecond, nil
}
