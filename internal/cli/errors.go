package cli

import (
	"errors"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func isNotConfigured(err error) bool  { return errors.Is(err, domain.ErrNotConfigured) }
func isAlreadyRunning(err error) bool { return errors.Is(err, domain.ErrAlreadyRunning) }
