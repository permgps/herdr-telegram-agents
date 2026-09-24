package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// UpdateInstaller runs only fixed argv derived from a revalidated release.
// Command output is deliberately discarded: installers and Git helpers can
// print credentials or repository content on failure.
type UpdateInstaller struct {
	StateDir   string
	HerdrBin   string
	Log        *slog.Logger
	RunCommand func(context.Context, string, string, ...string) error
	GitOutput  func(context.Context, string, ...string) (string, error)
	Inspect    func(context.Context, string, string) (domain.CheckoutState, error)
	OS         string
}

func (i *UpdateInstaller) run(ctx context.Context, dir, bin string, args ...string) error {
	if i.RunCommand != nil {
		return i.RunCommand(ctx, dir, bin, args...)
	}
	return updateCommand(ctx, dir, bin, args...)
}
func (i *UpdateInstaller) git(ctx context.Context, root string, args ...string) (string, error) {
	if i.GitOutput != nil {
		return i.GitOutput(ctx, root, args...)
	}
	return checkoutGit(ctx, root, args...)
}
func (i *UpdateInstaller) inspect(ctx context.Context, root, tag string) (domain.CheckoutState, error) {
	if i.Inspect != nil {
		return i.Inspect(ctx, root, tag)
	}
	return (&CheckoutInspector{Log: i.Log}).InspectCheckout(ctx, root, tag)
}
func (i *UpdateInstaller) hostOS() string {
	if i.OS != "" {
		return i.OS
	}
	return runtime.GOOS
}

func (i *UpdateInstaller) Backup(ctx context.Context, job domain.UpdateJob) (domain.UpdateJob, error) {
	if job.SourceKind == "github" {
		if job.OldCommit == "" {
			return job, fmt.Errorf("managed installation lacks rollback commit")
		}
		return job, nil
	}
	if job.SourceKind != "local" {
		return job, fmt.Errorf("unsupported update source")
	}
	checkout, err := i.inspect(ctx, job.SourceRoot, job.TargetTag)
	if err != nil {
		return job, err
	}
	if checkout.Dirty || checkout.Branch != "main" || !checkout.FastForward || checkout.TargetCommit != job.TargetCommit || (job.OldCommit != "" && checkout.Commit != job.OldCommit) {
		return job, fmt.Errorf("linked checkout changed since approval")
	}
	job.OldCommit = checkout.Commit
	name := updateBinaryName()
	backupDir := filepath.Join(i.StateDir, "update-backups", job.ID)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return job, err
	}
	backup := filepath.Join(backupDir, name)
	if err := copyUpdateFile(filepath.Join(job.SourceRoot, "bin", name), backup); err != nil {
		return job, err
	}
	job.OldBinaryBackup = backup
	if i.Log != nil {
		i.Log.Info("update backup staged", slog.String("job", job.ID), slog.String("commit", job.OldCommit))
	}
	return job, nil
}

func (i *UpdateInstaller) Install(ctx context.Context, job domain.UpdateJob) error {
	switch job.SourceKind {
	case "github":
		bin := i.HerdrBin
		if bin == "" {
			bin = "herdr"
		}
		if err := i.run(ctx, "", bin, "plugin", "install", "permgps/herdr-telegram-agents", "--ref", job.TargetTag, "--yes"); err != nil {
			return fmt.Errorf("managed install: %w", err)
		}
	case "local":
		if job.OldCommit == "" || job.OldBinaryBackup == "" {
			return fmt.Errorf("linked backup missing")
		}
		if err := i.run(ctx, job.SourceRoot, "git", "merge", "--ff-only", job.TargetCommit); err != nil {
			return fmt.Errorf("fast-forward checkout: %w", err)
		}
		if i.hostOS() == "windows" {
			if err := i.run(ctx, job.SourceRoot, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/install.ps1"); err != nil {
				return fmt.Errorf("checked installer: %w", err)
			}
		} else {
			if err := i.run(ctx, job.SourceRoot, "sh", "scripts/install.sh"); err != nil {
				return fmt.Errorf("checked installer: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported update source")
	}
	if i.Log != nil {
		i.Log.Info("update install completed", slog.String("job", job.ID), slog.String("tag", job.TargetTag))
	}
	return nil
}

func (i *UpdateInstaller) Verify(ctx context.Context, job domain.UpdateJob, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return verifyUpdateBinary(filepath.Join(root, "bin", updateBinaryName()), job.TargetChecksum)
}

func verifyUpdateBinary(path, expected string) error {
	if len(expected) != 64 {
		return fmt.Errorf("selected checksum missing")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(expected) {
		return fmt.Errorf("installed binary checksum differs from approved release")
	}
	return nil
}

func (i *UpdateInstaller) Rollback(ctx context.Context, job domain.UpdateJob) error {
	switch job.SourceKind {
	case "github":
		bin := i.HerdrBin
		if bin == "" {
			bin = "herdr"
		}
		if job.OldCommit == "" {
			return fmt.Errorf("old managed commit missing")
		}
		return i.run(ctx, "", bin, "plugin", "install", "permgps/herdr-telegram-agents", "--ref", job.OldCommit, "--yes")
	case "local":
		if job.OldCommit == "" || job.OldBinaryBackup == "" {
			return fmt.Errorf("old linked artifacts missing")
		}
		status, err := i.git(ctx, job.SourceRoot, "status", "--porcelain", "--untracked-files=all")
		if err != nil || status != "" {
			return fmt.Errorf("linked checkout changed during update; manual recovery required")
		}
		head, err := i.git(ctx, job.SourceRoot, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if head != job.TargetCommit && head != job.OldCommit {
			return fmt.Errorf("linked HEAD changed during update; manual recovery required")
		}
		if head != job.OldCommit {
			if err := i.run(ctx, job.SourceRoot, "git", "reset", "--hard", job.OldCommit); err != nil {
				return err
			}
		}
		return copyUpdateFile(job.OldBinaryBackup, filepath.Join(job.SourceRoot, "bin", updateBinaryName()))
	}
	return fmt.Errorf("unsupported update source")
}

func updateBinaryName() string {
	if runtime.GOOS == "windows" {
		return "herdr-tg.exe"
	}
	return "herdr-tg"
}

func copyUpdateFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(destination+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	defer os.Remove(destination + ".tmp")
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(destination)
	}
	return os.Rename(destination+".tmp", destination)
}

func updateCommand(ctx context.Context, dir, bin string, args ...string) error {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(callCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", strings.TrimSpace(filepath.Base(bin)), err)
	}
	return nil
}
