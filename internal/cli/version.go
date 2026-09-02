package cli

import (
	"fmt"
	"runtime"
)

// runVersion prints `herdr-tg <version> <GOOS>/<GOARCH> go<goversion>`.
func runVersion(ctx *runContext, _ []string) int {
	fmt.Fprintf(ctx.stdout, "herdr-tg %s %s/%s %s\n", ctx.version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return exitOK
}
