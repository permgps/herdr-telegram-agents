package domain

// CheckLevel is the outcome of one doctor check.
type CheckLevel int

const (
	// CheckOK means the check passed.
	CheckOK CheckLevel = iota
	// CheckWarn means the plugin works but something deserves attention.
	CheckWarn
	// CheckFail means the plugin cannot work until this is fixed.
	CheckFail
)

// Check is one line of the doctor report: a short name, the level and a
// one-line detail in English.
type Check struct {
	Name   string
	Level  CheckLevel
	Detail string
}

// Mark is the glyph shown before the check name: ✓, ! or ✗.
func (c Check) Mark() string {
	switch c.Level {
	case CheckWarn:
		return "!"
	case CheckFail:
		return "✗"
	default:
		return "✓"
	}
}

// Summarize counts the checks per level.
func Summarize(checks []Check) (ok, warn, fail int) {
	for _, c := range checks {
		switch c.Level {
		case CheckWarn:
			warn++
		case CheckFail:
			fail++
		default:
			ok++
		}
	}
	return ok, warn, fail
}
