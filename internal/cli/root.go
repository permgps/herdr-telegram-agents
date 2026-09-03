// Package cli implements the subcommands of the herdr-tg binary. It is the
// presentation layer: argument parsing, output, exit codes. It is one of the
// two packages allowed to read environment variables (LOG_LEVEL here).
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Exit codes shared by every subcommand.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// documented subcommands, in the order shown by usage. The `dev` subcommand
// is intentionally absent: it ships in the binary but stays undocumented.
var documentedCommands = []struct{ name, help string }{
	{"version", "print the binary version and platform"},
	{"startup", "[[startup]] hook: ensure the daemon is running"},
	{"daemon", "run the long-lived sync daemon in the foreground"},
	{"action <id>", "[[actions]] entrypoint: setup|start|stop|restart|status|resync|logs|doctor|send-test"},
	{"setup-pane", "[[panes]] popup: interactive setup wizard"},
	{"logs-pane", "[[panes]] overlay: tail of daemon.log"},
	{"doctor-pane", "[[panes]] overlay: one line per diagnostic check"},
	{"event", "[[events]] hook (fallback notifications)"},
}

// command is one subcommand handler. It receives the arguments after the
// subcommand name and returns an exit code.
type command func(ctx *runContext, args []string) int

// runContext carries the per-invocation dependencies every subcommand needs.
type runContext struct {
	version string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	log     *slog.Logger
}

// stdin is the reader interactive panes use; tests replace it.
var stdin io.Reader = os.Stdin

// Run dispatches args (os.Args[1:]) to a subcommand and returns the process
// exit code. It never calls os.Exit so it can be tested directly.
func Run(args []string, version string, stdout, stderr io.Writer) int {
	ctx := &runContext{
		version: version,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		log:     newLogger(stderr),
	}

	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	name := args[0]
	rest := args[1:]
	ctx.log.Debug("cli dispatch", slog.String("cmd", name), slog.Int("args_len", len(rest)))

	cmd, ok := commands()[name]
	if !ok {
		fmt.Fprintf(stderr, "herdr-tg: unknown subcommand %q\n\n", name)
		printUsage(stderr)
		return exitUsage
	}
	code := cmd(ctx, rest)
	ctx.log.Debug("cli done", slog.String("cmd", name), slog.Int("exit", code))
	return code
}

// commands builds the dispatch table. It is a function rather than a package
// variable so that handlers can reference Run-related helpers without an
// initialization cycle.
func commands() map[string]command {
	return map[string]command{
		"version":     runVersion,
		"startup":     runStartup,
		"daemon":      runDaemon,
		"action":      runAction,
		"setup-pane":  runSetupPane,
		"logs-pane":   runLogsPane,
		"doctor-pane": runDoctorPane,
		"event":       notImplemented("event"),
		"dev":         runDev,
	}
}

// notImplemented returns a placeholder handler for subcommands that arrive
// in later milestones. It exits 2 so a misconfigured manifest is noticed.
func notImplemented(name string) command {
	return func(ctx *runContext, _ []string) int {
		fmt.Fprintf(ctx.stderr, "herdr-tg %s: not implemented yet\n", name)
		return exitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: herdr-tg <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range documentedCommands {
		fmt.Fprintf(w, "  %-14s %s\n", c.name, c.help)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "environment:")
	fmt.Fprintln(w, "  LOG_LEVEL      debug|info|warn|error (default info)")
}

// newLogger builds the single *slog.Logger for a CLI invocation. It writes
// text lines to stderr; the rotating JSON file handler arrives with the
// daemon milestone. The level comes from LOG_LEVEL (default info).
func newLogger(stderr io.Writer) *slog.Logger {
	level := parseLevel(os.Getenv("LOG_LEVEL"))
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

// parseLevel maps a LOG_LEVEL value to a slog.Level; unknown values fall back
// to info so a typo never silences or floods the log.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
