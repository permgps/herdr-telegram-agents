package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Redactor masks secrets in text before it leaves the machine: API keys,
// tokens, passwords, private keys and the bot token itself. It is pure and
// safe for concurrent use; the daemon builds one per process.
//
// A matched key keeps a recognisable prefix and its last four characters
// (`sk-…a1b2`), key=value pairs keep the key (`password=[redacted]`), and
// the bot token and private-key blocks vanish whole (`[redacted]`). Every
// pattern carries a minimum length so ordinary words are left alone.
type Redactor struct {
	exact []string
}

// RedactionStats counts replacements per pattern name.
type RedactionStats map[string]int

// Total is the number of replacements across every pattern.
func (s RedactionStats) Total() int {
	n := 0
	for _, c := range s {
		n += c
	}
	return n
}

// String renders the counts as `name=count` pairs in name order, for logs.
func (s RedactionStats) String() string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, s[name]))
	}
	return strings.Join(parts, " ")
}

// redactedMark replaces a secret that must not stay recognisable.
const redactedMark = "[redacted]"

// exactMinLen is the shortest exact secret worth replacing; anything
// shorter would mask ordinary text.
const exactMinLen = 8

// keepEndsTail is how many trailing characters a masked key keeps.
const keepEndsTail = 4

type redactMode int

const (
	// redactWhole replaces the whole match with redactedMark.
	redactWhole redactMode = iota
	// redactKeepEnds keeps group 1 (the prefix) and the last four
	// characters of the match, with an ellipsis between.
	redactKeepEnds
	// redactKeyValue keeps groups 1 and 2 (key and separator) and replaces
	// group 3 (the value) with redactedMark.
	redactKeyValue
)

type redactRule struct {
	name string
	re   *regexp.Regexp
	mode redactMode
}

// redactRules run in order on the output of the previous rule; a
// replacement never matches a later rule because the ellipsis and the
// mark are outside every character class. Names may repeat: their counts
// merge in the stats.
var redactRules = []redactRule{
	{"privatekey", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`), redactWhole},
	{"telegram", regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}\b`), redactWhole},
	{"keyvalue", regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key)(\s*[=:]\s*['"]?)([^\s'"\[][^\s'"]{7,})`), redactKeyValue},
	{"openai", regexp.MustCompile(`\b(sk-)[A-Za-z0-9_-]{20,}`), redactKeepEnds},
	{"github", regexp.MustCompile(`\b(gh[poushr]_)[A-Za-z0-9]{36,}`), redactKeepEnds},
	{"github", regexp.MustCompile(`\b(github_pat_)[A-Za-z0-9_]{22,}`), redactKeepEnds},
	{"aws", regexp.MustCompile(`\b((?:AKIA|ASIA))[A-Z0-9]{16}\b`), redactKeepEnds},
	{"slack", regexp.MustCompile(`\b(xox[abprs]-)[A-Za-z0-9-]{10,}`), redactKeepEnds},
	{"gitlab", regexp.MustCompile(`\b(glpat-)[A-Za-z0-9_-]{20,}`), redactKeepEnds},
	{"google", regexp.MustCompile(`\b(AIza)[0-9A-Za-z_-]{35}\b`), redactKeepEnds},
	{"jwt", regexp.MustCompile(`\b(eyJ)[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), redactKeepEnds},
	{"bearer", regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]{16,}`), redactKeepEnds},
}

// NewRedactor returns a redactor that also replaces the given exact
// strings (the bot token); empty or short ones are ignored.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, s := range secrets {
		if len(s) >= exactMinLen {
			r.exact = append(r.exact, s)
		}
	}
	return r
}

// Redact returns text with every secret masked and the counts per pattern.
// An empty stats map means the text came back unchanged.
func (r *Redactor) Redact(text string) (string, RedactionStats) {
	stats := RedactionStats{}
	if text == "" {
		return text, stats
	}
	for _, s := range r.exact {
		if n := strings.Count(text, s); n > 0 {
			text = strings.ReplaceAll(text, s, redactedMark)
			stats["exact"] += n
		}
	}
	for _, rule := range redactRules {
		text = rule.apply(text, stats)
	}
	return text, stats
}

// apply rewrites every match of the rule in one pass so a replacement is
// never re-matched by the same rule.
func (rule redactRule) apply(text string, stats RedactionStats) string {
	matches := rule.re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range matches {
		b.WriteString(text[last:m[0]])
		b.WriteString(rule.replacement(text, m))
		last = m[1]
		stats[rule.name]++
	}
	b.WriteString(text[last:])
	return b.String()
}

func (rule redactRule) replacement(text string, m []int) string {
	whole := text[m[0]:m[1]]
	switch rule.mode {
	case redactKeepEnds:
		prefix := text[m[2]:m[3]]
		if len(whole) < len(prefix)+2*keepEndsTail {
			return redactedMark
		}
		return prefix + "…" + whole[len(whole)-keepEndsTail:]
	case redactKeyValue:
		return text[m[2]:m[3]] + text[m[4]:m[5]] + redactedMark
	default:
		return redactedMark
	}
}
