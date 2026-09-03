//go:build !darwin && !windows

package system

import (
	"context"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// idleFor has no source on this platform; presence stays "away".
func idleFor(context.Context) (time.Duration, error) {
	return 0, domain.ErrIdleUnsupported
}
