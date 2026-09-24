package domain

import "testing"

func TestVersionPrecedence(t *testing.T) {
	cases := []struct{ lower, higher string }{
		{"v1.0.0-alpha", "v1.0.0-alpha.1"},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta"},
		{"1.0.0-beta.2", "1.0.0-beta.11"},
		{"1.0.0-rc.1", "1.0.0"},
		{"v1.0.0", "v1.0.1"},
		{"v1.2.3-4-gabcdef-dirty", "v1.2.4"},
	}
	for _, tc := range cases {
		a, err := ParseVersion(tc.lower)
		if err != nil {
			t.Fatalf("%s: %v", tc.lower, err)
		}
		b, err := ParseVersion(tc.higher)
		if err != nil {
			t.Fatalf("%s: %v", tc.higher, err)
		}
		if a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Errorf("wrong precedence: %s < %s", tc.lower, tc.higher)
		}
	}
}

func TestInvalidVersions(t *testing.T) {
	for _, tag := range []string{"1.2", "v01.2.3", "1.2.3-01", "1.2.3-", "dev"} {
		if _, err := ParseVersion(tag); err == nil {
			t.Errorf("accepted %q", tag)
		}
	}
}
