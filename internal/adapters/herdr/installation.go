package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

var manifestVersion = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"\s*$`)

// InstallationReader reads the exact plugin.list JSON envelope confirmed on
// Herdr 0.9.1. Unknown source kinds remain visible to the app as blockers.
type InstallationReader struct {
	Bin, ExpectedRoot string
	Status            func(context.Context) (string, error)
	Log               *slog.Logger
}

func (r *InstallationReader) ReadInstallation(ctx context.Context) (domain.PluginInstallation, error) {
	bin := r.Bin
	if bin == "" {
		bin = "herdr"
	}
	out, err := installationCommand(ctx, "", bin, "plugin", "list", "--plugin", "permgps.telegram-agents", "--json")
	if err != nil {
		return domain.PluginInstallation{}, fmt.Errorf("herdr plugin list: %w", err)
	}
	var envelope struct {
		Result struct {
			Plugins []struct {
				ID      string `json:"plugin_id"`
				Root    string `json:"plugin_root"`
				Version string `json:"version"`
				Source  struct {
					Kind           string `json:"kind"`
					Owner          string `json:"owner"`
					Repo           string `json:"repo"`
					RequestedRef   string `json:"requested_ref"`
					ResolvedCommit string `json:"resolved_commit"`
					ManagedPath    string `json:"managed_path"`
				} `json:"source"`
			} `json:"plugins"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return domain.PluginInstallation{}, fmt.Errorf("decode plugin list: %w", err)
	}
	if len(envelope.Result.Plugins) != 1 || envelope.Result.Plugins[0].ID != "permgps.telegram-agents" {
		return domain.PluginInstallation{}, fmt.Errorf("plugin registration missing or ambiguous")
	}
	p := envelope.Result.Plugins[0]
	if p.Root == "" || p.Source.Kind == "" {
		return domain.PluginInstallation{}, fmt.Errorf("plugin registration missing root or source")
	}
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return domain.PluginInstallation{}, fmt.Errorf("resolve plugin root: %w", err)
	}
	if r.ExpectedRoot != "" {
		expected, err := filepath.EvalSymlinks(r.ExpectedRoot)
		if err != nil || root != expected {
			return domain.PluginInstallation{}, fmt.Errorf("registered plugin root changed")
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, "herdr-plugin.toml"))
	if err != nil {
		return domain.PluginInstallation{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	m := manifestVersion.FindSubmatch(manifest)
	if m == nil {
		return domain.PluginInstallation{}, fmt.Errorf("plugin manifest has no version")
	}
	binName := "herdr-tg"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	versionLine, err := installationCommand(ctx, root, filepath.Join(root, "bin", binName), "version")
	if err != nil {
		return domain.PluginInstallation{}, fmt.Errorf("read plugin binary version: %w", err)
	}
	fields := strings.Fields(string(versionLine))
	if len(fields) < 2 || fields[0] != "herdr-tg" {
		return domain.PluginInstallation{}, fmt.Errorf("unexpected binary version output")
	}
	result := domain.PluginInstallation{
		Root: root, ManifestVersion: string(m[1]), BinaryVersion: fields[1],
		SourceKind: p.Source.Kind, Owner: p.Source.Owner, Repo: p.Source.Repo,
		RequestedRef: p.Source.RequestedRef, ResolvedCommit: p.Source.ResolvedCommit,
		ManagedPath: p.Source.ManagedPath,
	}
	if p.Version != result.ManifestVersion {
		return domain.PluginInstallation{}, fmt.Errorf("herdr and on-disk manifest versions differ")
	}
	if r.Status != nil {
		line, err := r.Status(ctx)
		if err == nil && strings.Contains(line, "version=") {
			for _, field := range strings.Fields(line) {
				if value, ok := strings.CutPrefix(field, "version="); ok {
					result.RunningVersion = value
					result.Running = true
					break
				}
			}
		}
	}
	if r.Log != nil {
		r.Log.Info("installation inspected", slog.String("source", result.SourceKind), slog.String("manifest_version", result.ManifestVersion), slog.String("binary_version", result.BinaryVersion), slog.Bool("running", result.Running))
	}
	return result, nil
}

func installationCommand(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("command failed: %w", err)
	}
	if stdout.Len() > 2<<20 {
		return nil, fmt.Errorf("command output too large")
	}
	return stdout.Bytes(), nil
}
