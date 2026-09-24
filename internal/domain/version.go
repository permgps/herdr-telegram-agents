package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
var describePattern = regexp.MustCompile(`^(.+)-([0-9]+)-g[0-9a-f]+(?:-dirty)?$`)

// Version is a semantic version. Build metadata does not affect precedence.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseVersion accepts a release tag, or a git-describe binary version such
// as v1.2.3-4-gabcdef-dirty. A described binary is compared at its base tag;
// callers must check the dirty suffix separately before permitting an update.
func ParseVersion(raw string) (Version, error) {
	if m := describePattern.FindStringSubmatch(raw); m != nil {
		raw = m[1]
	}
	m := versionPattern.FindStringSubmatch(raw)
	if m == nil {
		return Version{}, fmt.Errorf("invalid semantic version %q", raw)
	}
	var v Version
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version number overflow: %w", err)
		}
		*dst = n
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
		for _, p := range v.Pre {
			if p == "" || (len(p) > 1 && p[0] == '0' && numeric(p)) {
				return Version{}, fmt.Errorf("invalid prerelease identifier %q", p)
			}
		}
	}
	return v, nil
}

func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Compare returns -1, 0, or 1 according to SemVer precedence.
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]uint64{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(v.Pre) == 0 && len(other.Pre) > 0 {
		return 1
	}
	if len(v.Pre) > 0 && len(other.Pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(other.Pre); i++ {
		a, b := v.Pre[i], other.Pre[i]
		an, bn := numeric(a), numeric(b)
		switch {
		case an && bn:
			// Arbitrarily long numeric identifiers compare by length, then text.
			if len(a) != len(b) {
				if len(a) < len(b) {
					return -1
				}
				return 1
			}
		case an && !bn:
			return -1
		case !an && bn:
			return 1
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
	}
	if len(v.Pre) < len(other.Pre) {
		return -1
	}
	if len(v.Pre) > len(other.Pre) {
		return 1
	}
	return 0
}

// Release is a published GitHub release selected for this host.
type Release struct {
	Tag          string
	Version      Version
	AssetURL     string
	ChecksumsURL string
}

// ReleaseManifest is the manifest pinned by the selected GitHub tag.
type ReleaseManifest struct {
	Version         string
	MinHerdrVersion string
}
