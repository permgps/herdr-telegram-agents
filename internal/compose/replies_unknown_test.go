package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// TestReplySourcesUnknownKindIsUnsupported: every reader rejects a kind it
// does not know with ErrUnsupportedAgent, so the chain reports an expected
// fallback (nothing for the unreadable-replies notice to count).
func TestReplySourcesUnknownKindIsUnsupported(t *testing.T) {
	src := replySources(
		func(context.Context, string) (domain.SessionTuple, error) { return domain.SessionTuple{}, nil },
		func(context.Context, string) ([]byte, error) { return nil, nil },
		func(context.Context, string) ([]int, error) { return nil, nil },
		nil, nil)
	agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Kind: "gemini", Cwd: t.TempDir()}
	_, err := src.LastReply(context.Background(), agent)
	if !errors.Is(err, domain.ErrUnsupportedAgent) {
		t.Fatalf("err = %v, want ErrUnsupportedAgent", err)
	}
}
