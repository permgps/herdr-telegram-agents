package system

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// idleTimeout bounds one sample of the idle source (the ioreg call on
// macOS); the presence tracker asks every ten seconds.
const idleTimeout = 3 * time.Second

// IdleSource reports how long the keyboard and mouse of this machine have
// been untouched. macOS reads HIDIdleTime through ioreg, Windows calls
// GetLastInputInfo; every other platform answers ErrIdleUnsupported.
type IdleSource struct {
	log     *slog.Logger
	timeout time.Duration
}

var _ domain.IdleSource = (*IdleSource)(nil)

// NewIdleSource builds the source for the current platform.
func NewIdleSource(log *slog.Logger) *IdleSource {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &IdleSource{log: log, timeout: idleTimeout}
}

// Idle samples the platform source once. ErrIdleUnsupported passes through
// so the caller can recognise it with errors.Is; other failures are wrapped
// and logged at debug (the tracker rate-limits the warning).
func (s *IdleSource) Idle(ctx context.Context) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	d, err := idleFor(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrIdleUnsupported) {
			return 0, err
		}
		s.log.Debug("input idle sample failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("input idle: %w", err)
	}
	return d, nil
}
