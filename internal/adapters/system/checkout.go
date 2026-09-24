package system

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// CheckoutInspector validates linked installations before any update button
// is offered. Git invocations have fixed argument structure and a deadline.
type CheckoutInspector struct{ Log *slog.Logger }

func (i *CheckoutInspector) InspectCheckout(ctx context.Context, root, tag string) (domain.CheckoutState, error) {
	if _, err := domain.ParseVersion(tag); err != nil {
		return domain.CheckoutState{}, err
	}
	result := domain.CheckoutState{}
	var err error
	if result.Branch, err = checkoutGit(ctx, root, "branch", "--show-current"); err != nil {
		return result, err
	}
	if result.Origin, err = checkoutGit(ctx, root, "remote", "get-url", "origin"); err != nil {
		return result, err
	}
	if result.Commit, err = checkoutGit(ctx, root, "rev-parse", "HEAD"); err != nil {
		return result, err
	}
	status, err := checkoutGit(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	result.Dirty = status != ""
	if result.Branch != "main" || result.Dirty || !expectedOrigin(result.Origin) {
		return result, nil
	}
	// Fetch only known refs. The tag's peeled commit must be on the remote
	// mainline and at or ahead of the current HEAD.
	if _, err := checkoutGit(ctx, root, "fetch", "--no-tags", "origin", "refs/heads/main:refs/remotes/origin/main", "refs/tags/"+tag); err != nil {
		return result, err
	}
	if result.TargetCommit, err = checkoutGit(ctx, root, "rev-parse", "refs/tags/"+tag+"^{commit}"); err != nil {
		return result, err
	}
	if _, err := checkoutGit(ctx, root, "merge-base", "--is-ancestor", "HEAD", result.TargetCommit); err != nil {
		return result, nil
	}
	if _, err := checkoutGit(ctx, root, "merge-base", "--is-ancestor", result.TargetCommit, "refs/remotes/origin/main"); err != nil {
		return result, nil
	}
	result.FastForward = true
	if i.Log != nil {
		i.Log.Info("linked checkout inspected", slog.String("branch", result.Branch), slog.Bool("fast_forward", true), slog.String("tag", tag))
	}
	return result, nil
}

func expectedOrigin(raw string) bool {
	return raw == "git@github.com:permgps/herdr-telegram-agents.git" ||
		raw == "git@github.com:permgps/herdr-telegram-agents" ||
		raw == "https://github.com/permgps/herdr-telegram-agents.git" ||
		raw == "https://github.com/permgps/herdr-telegram-agents"
}

func checkoutGit(ctx context.Context, root string, args ...string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %w", args[0], err)
	}
	if stdout.Len() > 1<<20 {
		return "", fmt.Errorf("git %s output too large", args[0])
	}
	return strings.TrimSpace(stdout.String()), nil
}
