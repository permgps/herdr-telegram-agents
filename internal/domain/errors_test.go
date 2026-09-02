package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestSentinelsSurviveWrapping(t *testing.T) {
	sentinels := []error{
		domain.ErrNotConfigured, domain.ErrAgentGone, domain.ErrTopicGone, domain.ErrTopicClosed,
		domain.ErrForbidden, domain.ErrBotUnauthorized, domain.ErrPollerConflict, domain.ErrChatMigrated,
		domain.ErrDisconnected, domain.ErrAlreadyRunning, domain.ErrNotRunning, domain.ErrUnsupportedPlatform,
	}
	for _, s := range sentinels {
		wrapped := fmt.Errorf("outer: %w", s)
		if !errors.Is(wrapped, s) {
			t.Fatalf("errors.Is failed for %v", s)
		}
		for _, other := range sentinels {
			if other != s && errors.Is(wrapped, other) {
				t.Fatalf("%v matched unrelated %v", s, other)
			}
		}
	}
}
