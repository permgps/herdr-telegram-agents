package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// UpdateEligibility is the read-only local preflight result for one release.
// A blocker is suitable for the options panel; it never authorizes install.
type UpdateEligibility struct {
	Installation domain.PluginInstallation
	Checkout     domain.CheckoutState
	Blocker      string
	Code         string
}

type UpdatePreflight struct {
	Installation domain.InstallationReader
	Checkout     domain.CheckoutInspector
	ExpectedRoot string
	Log          *slog.Logger
	History      domain.UpdateJobStore
}

func (p *UpdatePreflight) Check(ctx context.Context, release domain.Release) (UpdateEligibility, error) {
	if p.Installation == nil {
		return UpdateEligibility{}, fmt.Errorf("installation reader unavailable")
	}
	install, err := p.Installation.ReadInstallation(ctx)
	if err != nil {
		return UpdateEligibility{}, fmt.Errorf("inspect installation: %w", err)
	}
	result := UpdateEligibility{Installation: install}
	block := func(code, message string) (UpdateEligibility, error) {
		result.Code, result.Blocker = code, message
		if p.Log != nil {
			p.Log.Warn("update ineligible", slog.String("code", code), slog.String("tag", release.Tag))
		}
		return result, nil
	}
	if p.ExpectedRoot != "" {
		root, err1 := filepath.EvalSymlinks(p.ExpectedRoot)
		registered, err2 := filepath.EvalSymlinks(install.Root)
		if err1 != nil || err2 != nil || root != registered {
			return block("root_changed", "Herdr now points to a different plugin checkout. Restart this panel.")
		}
	}
	manifest, err := domain.ParseVersion(install.ManifestVersion)
	if err != nil {
		return block("manifest_version", "The installed manifest has an invalid version.")
	}
	binary, err := domain.ParseVersion(install.BinaryVersion)
	if err != nil {
		return block("binary_version", "The installed binary has an invalid version.")
	}
	if binary.Compare(release.Version) >= 0 || manifest.Compare(release.Version) >= 0 {
		return block("already_installed", "The release is already on disk. Restart the daemon if it still reports an older version.")
	}
	if install.Running && install.RunningVersion != "" {
		if running, err := domain.ParseVersion(install.RunningVersion); err == nil && running.Compare(release.Version) >= 0 {
			return block("running_newer", "The running daemon is already at this release or newer.")
		}
	}
	switch install.SourceKind {
	case "github":
		if !strings.EqualFold(install.Owner, "permgps") || !strings.EqualFold(install.Repo, "herdr-telegram-agents") || install.ManagedPath == "" {
			return block("wrong_repository", "This managed plugin came from another repository.")
		}
		managed, err1 := filepath.EvalSymlinks(install.ManagedPath)
		root, err2 := filepath.EvalSymlinks(install.Root)
		rel, err3 := filepath.Rel(managed, root)
		if err1 != nil || err2 != nil || err3 != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return block("managed_root", "The managed plugin root is outside its recorded checkout.")
		}
		if install.ResolvedCommit == "" {
			return block("rollback_unavailable", "Herdr did not record the installed commit; update manually.")
		}
		if install.RequestedRef != "" && install.RequestedRef != "main" && install.RequestedRef != "master" {
			owned := false
			if p.History != nil {
				if previous, err := p.History.Load(ctx); err == nil {
					owned = previous.Phase == "succeeded" && previous.TargetTag == install.RequestedRef && previous.NewCommit == install.ResolvedCommit
				}
			}
			if !owned {
				return block("pinned", "This managed installation is pinned. Update it manually with herdr plugin install --ref.")
			}
		}
	case "local":
		if p.Checkout == nil {
			return UpdateEligibility{}, fmt.Errorf("checkout inspector unavailable")
		}
		checkout, err := p.Checkout.InspectCheckout(ctx, install.Root, release.Tag)
		if err != nil {
			return UpdateEligibility{}, fmt.Errorf("inspect checkout: %w", err)
		}
		result.Checkout = checkout
		if !strings.EqualFold(checkout.Origin, "git@github.com:permgps/herdr-telegram-agents.git") &&
			!strings.EqualFold(checkout.Origin, "git@github.com:permgps/herdr-telegram-agents") &&
			!strings.EqualFold(checkout.Origin, "https://github.com/permgps/herdr-telegram-agents.git") &&
			!strings.EqualFold(checkout.Origin, "https://github.com/permgps/herdr-telegram-agents") {
			return block("wrong_repository", "The linked checkout has another Git origin.")
		}
		if checkout.Branch != "main" {
			return block("branch", "Switch the linked checkout to main before updating.")
		}
		if checkout.Dirty {
			return block("dirty", "Commit or stash changes in the linked checkout before updating.")
		}
		if !checkout.FastForward {
			return block("non_fast_forward", "The release is not a fast-forward of this checkout's main branch.")
		}
	default:
		return block("source_kind", "This plugin source cannot be updated from Telegram.")
	}
	if p.Log != nil {
		p.Log.Info("update eligible", slog.String("tag", release.Tag), slog.String("source", install.SourceKind))
	}
	return result, nil
}
