//go:build windows

package cli

import "log/slog"

// watchResync is a no-op on Windows: there is no SIGHUP, and the control
// mechanism is decided in the Windows milestone.
func watchResync(_ func(), log *slog.Logger) (stop func()) {
	log.Debug("resync signal not supported on windows")
	return func() {}
}
