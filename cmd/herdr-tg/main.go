// Command herdr-tg is the single binary behind the Telegram Agents Herdr
// plugin. Every manifest entrypoint (startup hook, actions, panes) and the
// long-running daemon are subcommands of this executable.
package main

import (
	"os"

	"github.com/permgps/herdr-telegram-agents/internal/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version, os.Stdout, os.Stderr))
}
