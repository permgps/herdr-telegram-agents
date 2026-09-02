package domain

import "context"

// BotIdentity is what Telegram reports about the bot behind a token.
type BotIdentity struct {
	ID       int64
	Username string
}

// GroupCandidate is a forum supergroup where the bot was just promoted to
// an administrator who may manage topics. FromID is the user who granted
// the rights; the wizard records them as the operator.
type GroupCandidate struct {
	ChatID       int64
	Title        string
	FromID       int64
	FromUsername string
}

// SetupLink is the deep link the wizard shows: opening it and pressing
// Start makes the bot offer the group picker.
func SetupLink(botUsername string) string {
	return "https://t.me/" + botUsername + "?start=setup"
}

// SetupProbe is the slice of Telegram the setup wizard needs: prove the
// token works and watch for group promotions. Candidates delivers until ctx
// is done; the channel is closed afterwards.
type SetupProbe interface {
	Identity(ctx context.Context) (BotIdentity, error)
	Candidates(ctx context.Context) (<-chan GroupCandidate, error)
}

// SetupUI is the interactive surface of the wizard. The CLI implements it
// over stdin/stdout; tests script it.
type SetupUI interface {
	// Print shows one line of text.
	Print(text string)
	// Ask prompts for a line of input.
	Ask(prompt string) (string, error)
	// AskSecret prompts for a value that must not be echoed back later.
	AskSecret(prompt string) (string, error)
	// Confirm asks a yes/no question; the default answer is no.
	Confirm(prompt string) (bool, error)
	// Choose asks for one of the options and returns its index.
	Choose(prompt string, options []string) (int, error)
	// OpenLink tries to open an http(s) link in the user's browser. It is
	// best effort: the wizard prints the link regardless.
	OpenLink(url string) error
}
