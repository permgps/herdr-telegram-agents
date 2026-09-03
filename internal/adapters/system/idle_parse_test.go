package system

import (
	"errors"
	"testing"
	"time"
)

func TestParseHIDIdleTime(t *testing.T) {
	real := []byte(`+-o IOHIDSystem  <class IOHIDSystem, id 0x100000456, registered, matched, active, busy 0 (0 ms), retain 12>
    {
      "IOClass" = "IOHIDSystem"
      "HIDIdleTime" = 194607375
      "HIDParameters" = {"HIDPointerAcceleration"=45056}
    }
`)
	cases := []struct {
		name string
		in   []byte
		want time.Duration
		err  error
	}{
		{"real excerpt", real, 194607375 * time.Nanosecond, nil},
		{"zero", []byte(`"HIDIdleTime" = 0`), 0, nil},
		{"first match wins", []byte("\"HIDIdleTime\" = 5000000000\n\"HIDIdleTime\" = 1"), 5 * time.Second, nil},
		{"missing", []byte(`"HIDParameters" = {}`), 0, errNoHIDIdleTime},
		{"garbage", []byte("nothing here"), 0, errNoHIDIdleTime},
	}
	for _, tc := range cases {
		got, err := parseHIDIdleTime(tc.in)
		if !errors.Is(err, tc.err) || got != tc.want {
			t.Errorf("%s: parseHIDIdleTime = %v, %v; want %v, %v", tc.name, got, err, tc.want, tc.err)
		}
	}
}
