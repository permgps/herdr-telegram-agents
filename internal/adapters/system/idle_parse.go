package system

import (
	"errors"
	"regexp"
	"strconv"
	"time"
)

// hidIdleTime matches the IOHIDSystem property ioreg prints, in nanoseconds:
//
//	"HIDIdleTime" = 194607375
var hidIdleTime = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

var errNoHIDIdleTime = errors.New("HIDIdleTime not found in ioreg output")

// parseHIDIdleTime extracts the first HIDIdleTime value from ioreg output.
func parseHIDIdleTime(out []byte) (time.Duration, error) {
	m := hidIdleTime.FindSubmatch(out)
	if m == nil {
		return 0, errNoHIDIdleTime
	}
	ns, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(ns), nil
}
