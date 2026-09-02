//go:build !windows

package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// watchResync calls resync on every SIGHUP until the returned stop
// function runs. The resync action sends that signal.
func watchResync(resync func(), log *slog.Logger) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				log.Info("SIGHUP received, resyncing")
				resync()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
