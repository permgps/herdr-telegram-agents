package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type installReader struct{ value domain.PluginInstallation }

func (r installReader) ReadInstallation(context.Context) (domain.PluginInstallation, error) {
	return r.value, nil
}

type checkoutReader struct{ value domain.CheckoutState }

func (r checkoutReader) InspectCheckout(context.Context, string, string) (domain.CheckoutState, error) {
	return r.value, nil
}

func TestUpdatePreflightBlockers(t *testing.T) {
	version, _ := domain.ParseVersion("1.2.0")
	release := domain.Release{Tag: "v1.2.0", Version: version}
	managed := t.TempDir()
	root := filepath.Join(managed, "plugin")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	base := domain.PluginInstallation{Root: root, ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", SourceKind: "github", Owner: "permgps", Repo: "herdr-telegram-agents", ManagedPath: managed, ResolvedCommit: "old-commit"}
	local := domain.CheckoutState{Branch: "main", Origin: "https://github.com/permgps/herdr-telegram-agents.git", Commit: "old", TargetCommit: "new", FastForward: true}
	for _, tc := range []struct {
		name   string
		change func(*domain.PluginInstallation, *domain.CheckoutState)
		want   string
	}{
		{"managed", func(*domain.PluginInstallation, *domain.CheckoutState) {}, ""},
		{"pin", func(i *domain.PluginInstallation, _ *domain.CheckoutState) { i.RequestedRef = "v1.0.0" }, "pinned"},
		{"wrong repo", func(i *domain.PluginInstallation, _ *domain.CheckoutState) { i.Repo = "another" }, "wrong_repository"},
		{"local", func(i *domain.PluginInstallation, _ *domain.CheckoutState) { i.SourceKind = "local" }, ""},
		{"dirty", func(i *domain.PluginInstallation, c *domain.CheckoutState) { i.SourceKind = "local"; c.Dirty = true }, "dirty"},
		{"detached", func(i *domain.PluginInstallation, c *domain.CheckoutState) { i.SourceKind = "local"; c.Branch = "" }, "branch"},
		{"non fast forward", func(i *domain.PluginInstallation, c *domain.CheckoutState) {
			i.SourceKind = "local"
			c.FastForward = false
		}, "non_fast_forward"},
		{"disk newer than daemon", func(i *domain.PluginInstallation, _ *domain.CheckoutState) {
			i.BinaryVersion = "1.2.0"
			i.RunningVersion = "1.0.0"
			i.Running = true
		}, "already_installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install, checkout := base, local
			tc.change(&install, &checkout)
			preflight := UpdatePreflight{Installation: installReader{install}, Checkout: checkoutReader{checkout}}
			result, err := preflight.Check(context.Background(), release)
			if err != nil || result.Code != tc.want {
				t.Fatalf("code=%s want=%s err=%v", result.Code, tc.want, err)
			}
		})
	}
}
