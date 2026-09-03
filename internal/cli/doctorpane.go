package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// doctorPaneTimeout bounds the whole doctor run; every check has its own
// shorter limit inside compose.Doctor.
const doctorPaneTimeout = 30 * time.Second

// runDoctorPane handles `doctor-pane`: it runs every diagnostic check,
// prints one line per check and a summary, then waits for Esc or q (or
// the end of stdin) so the overlay stays readable.
func runDoctorPane(rc *runContext, _ []string) int {
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg doctor-pane: %v\n", err)
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithTimeout(ctx, doctorPaneTimeout)
	defer cancel()

	rc.log.Info("doctor pane opened", slog.String("state_dir", env.StateDir))
	fmt.Fprintln(rc.stdout, "running checks…")
	checks := wire.buildDoctor(env, rc.version, rc.log).Run(runCtx)
	fmt.Fprint(rc.stdout, compose.RenderChecks(rc.version, checks))
	fmt.Fprintln(rc.stdout, "(Esc or q to close)")
	rc.log.Info("doctor done", slog.String("summary", checksSummary(checks)))

	waitCtx, done := context.WithCancel(ctx)
	defer done()
	go watchQuitKeys(rc.stdin, done)
	<-waitCtx.Done()
	rc.log.Debug("doctor pane closed")
	return exitOK
}

// checksSummary is the exit-code-free one-line summary logged after a run.
func checksSummary(checks []domain.Check) string {
	ok, warn, fail := domain.Summarize(checks)
	return fmt.Sprintf("%d ok, %d warnings, %d failures", ok, warn, fail)
}
