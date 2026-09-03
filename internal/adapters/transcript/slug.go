package transcript

import (
	"strings"
	"unicode/utf16"
)

// projectSlug is the directory name Claude Code derives from a working
// directory: every character outside [A-Za-z0-9] becomes "-", so
// "/Users/op/Projects/My/herdr_tg" is "-Users-op-Projects-My-herdr-tg".
// Claude Code applies the rule per UTF-16 code unit (a JavaScript string
// replace), so a character outside the BMP becomes two dashes. Verified
// for "/" and "_" against a live ~/.claude/projects on 2026-09-03; "." and
// spaces follow the same rule.
func projectSlug(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			units := utf16.RuneLen(r)
			if units < 1 {
				units = 1
			}
			b.WriteString(strings.Repeat("-", units))
		}
	}
	return b.String()
}
