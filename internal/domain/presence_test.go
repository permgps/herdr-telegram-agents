package domain

import (
	"testing"
	"time"
)

func TestPresenceStateWord(t *testing.T) {
	cases := []struct {
		name string
		s    PresenceState
		want string
	}{
		{"disabled", PresenceState{Enabled: false, Supported: true, AtDesk: true}, "off"},
		{"unsupported", PresenceState{Enabled: true, Supported: false, AtDesk: false}, "off"},
		{"unsupported with manual away", PresenceState{Enabled: true, Supported: false, ManualAway: true}, "off"},
		{"quiet", PresenceState{Enabled: true, Supported: true, AtDesk: true, Quiet: true}, "on"},
		{"away", PresenceState{Enabled: true, Supported: true, AtDesk: false}, "away"},
		{"manual away", PresenceState{Enabled: true, Supported: true, AtDesk: true, ManualAway: true, Until: time.Unix(1, 0)}, "away-manual"},
	}
	for _, tc := range cases {
		if got := tc.s.Word(); got != tc.want {
			t.Errorf("%s: Word() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
