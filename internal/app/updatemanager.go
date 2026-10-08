package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

var ErrUpdateIntent = errors.New("update button expired or installation changed")

// UpdateManager performs both presses. It never mutates installation files;
// the worker receives only a consumed and revalidated result.
type UpdateManager struct {
	Releases  domain.ReleaseSource
	Preflight *UpdatePreflight
	Herdr     domain.HerdrProber
	Now       func() time.Time
	Log       *slog.Logger
	mu        sync.Mutex
	intents   map[string]domain.UpdateIntent
}

func (m *UpdateManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *UpdateManager) Check(ctx context.Context, chatID int64, panelID int, operatorID int64) (domain.UpdateCheck, error) {
	result, eligibility, err := m.check(ctx)
	if err != nil {
		return result, err
	}
	if !result.Available || result.Blocker != "" {
		return result, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.UpdateCheck{}, fmt.Errorf("create update intent: %w", err)
	}
	intent := domain.UpdateIntent{
		ID: hex.EncodeToString(random[:]), ChatID: chatID, PanelMessageID: panelID,
		OperatorID: operatorID, Tag: result.Release.Tag,
		Fingerprint: fingerprint(eligibility, result), ExpiresAt: m.now().Add(5 * time.Minute),
	}
	m.mu.Lock()
	// A new check replaces older approvals for this panel.
	for id, old := range m.intents {
		if old.ChatID == chatID && old.PanelMessageID == panelID {
			delete(m.intents, id)
		}
	}
	if m.intents == nil {
		m.intents = make(map[string]domain.UpdateIntent)
	}
	m.intents[intent.ID] = intent
	m.mu.Unlock()
	result.IntentID = intent.ID
	if m.Log != nil {
		m.Log.Info("update intent created", slog.String("tag", intent.Tag), slog.Int("panel", panelID), slog.Int64("operator", operatorID))
	}
	return result, nil
}

// Consume takes the intent at most once before any blocking recheck, so rapid
// duplicate callbacks cannot launch two installations.
func (m *UpdateManager) Consume(ctx context.Context, id string, chatID int64, panelID int, operatorID int64) (domain.UpdateCheck, error) {
	m.mu.Lock()
	intent, exists := m.intents[id]
	delete(m.intents, id)
	m.mu.Unlock()
	if !exists || intent.ChatID != chatID || intent.PanelMessageID != panelID || intent.OperatorID != operatorID || !m.now().Before(intent.ExpiresAt) {
		if m.Log != nil {
			m.Log.Warn("update intent rejected", slog.String("reason", "missing_expired_or_binding"))
		}
		return domain.UpdateCheck{}, ErrUpdateIntent
	}
	result, eligibility, err := m.check(ctx)
	if err != nil {
		return result, err
	}
	if !result.Available || result.Blocker != "" || result.Release.Tag != intent.Tag || fingerprint(eligibility, result) != intent.Fingerprint {
		if m.Log != nil {
			m.Log.Warn("update intent rejected", slog.String("reason", "target_or_source_changed"), slog.String("tag", intent.Tag))
		}
		return domain.UpdateCheck{}, ErrUpdateIntent
	}
	if m.Log != nil {
		m.Log.Info("update intent consumed", slog.String("tag", intent.Tag))
	}
	return result, nil
}

func (m *UpdateManager) check(ctx context.Context) (domain.UpdateCheck, UpdateEligibility, error) {
	if m.Preflight == nil || m.Preflight.Installation == nil || m.Releases == nil || m.Herdr == nil {
		return domain.UpdateCheck{}, UpdateEligibility{}, fmt.Errorf("update checker not configured")
	}
	install, err := m.Preflight.Installation.ReadInstallation(ctx)
	if err != nil {
		return domain.UpdateCheck{}, UpdateEligibility{}, err
	}
	result := domain.UpdateCheck{Installed: install.ManifestVersion, Running: install.RunningVersion}
	current, err := domain.ParseVersion(install.ManifestVersion)
	if err != nil {
		return result, UpdateEligibility{}, fmt.Errorf("installed version: %w", err)
	}
	if binary, err := domain.ParseVersion(install.BinaryVersion); err == nil && binary.Compare(current) > 0 {
		current = binary
	}
	release, found, err := m.Releases.Latest(ctx, current)
	if err != nil {
		return result, UpdateEligibility{}, err
	}
	if !found {
		if install.RunningVersion != "" {
			if running, err := domain.ParseVersion(install.RunningVersion); err == nil && running.Compare(current) < 0 {
				result.BlockerCode, result.Blocker = "restart_needed", "A newer plugin is already installed. Restart the daemon to use it."
			}
		}
		return result, UpdateEligibility{}, nil
	}
	result.Release, result.Available = release, true
	statement, err := m.Releases.Statement(ctx, release)
	switch {
	case errors.Is(err, domain.ErrReleaseUnsigned):
		m.logBlocker(slog.LevelInfo, "unsigned", release.Tag)
		result.BlockerCode, result.Blocker = "unsigned", "This release is not signed yet. Try again in a few minutes or update manually."
		return result, UpdateEligibility{}, nil
	case errors.Is(err, domain.ErrReleaseSignature):
		m.logBlocker(slog.LevelWarn, "bad_signature", release.Tag)
		result.BlockerCode, result.Blocker = "bad_signature", "This release's signature does not verify. Do not install it; report it to the maintainer."
		return result, UpdateEligibility{}, nil
	case err != nil:
		return result, UpdateEligibility{}, err
	}
	result.Checksum, result.Commit, result.Signer = statement.Checksum, statement.Commit, statement.Signer
	manifest, err := m.Releases.Manifest(ctx, release)
	if err != nil {
		return result, UpdateEligibility{}, err
	}
	info, err := m.Herdr.Ping(ctx)
	if err != nil {
		return result, UpdateEligibility{}, err
	}
	minimum, err := domain.ParseVersion(manifest.MinHerdrVersion)
	if err != nil {
		return result, UpdateEligibility{}, fmt.Errorf("release minimum herdr version: %w", err)
	}
	installedHerdr, err := domain.ParseVersion(info.Version)
	if err != nil {
		return result, UpdateEligibility{}, fmt.Errorf("herdr version: %w", err)
	}
	if installedHerdr.Compare(minimum) < 0 {
		result.BlockerCode, result.Blocker = "herdr_too_old", "Update Herdr before installing this plugin release."
		return result, UpdateEligibility{}, nil
	}
	eligibility, err := m.Preflight.Check(ctx, release)
	if err != nil {
		return result, eligibility, err
	}
	result.BlockerCode, result.Blocker = eligibility.Code, eligibility.Blocker
	result.Installation, result.Checkout = eligibility.Installation, eligibility.Checkout
	// A linked checkout fast-forwards to the tag it fetched; that tag must
	// still point at the commit the owner signed.
	if result.Blocker == "" && eligibility.Installation.SourceKind == "local" && eligibility.Checkout.TargetCommit != statement.Commit {
		m.logBlocker(slog.LevelWarn, "commit_mismatch", release.Tag)
		result.BlockerCode, result.Blocker = "commit_mismatch", "The release tag no longer points at the signed commit."
	}
	if m.Log != nil {
		m.Log.Info("update check", slog.String("tag", release.Tag), slog.String("commit", shortCommit(statement.Commit)),
			slog.String("signer", statement.Signer), slog.String("blocker", result.BlockerCode))
	}
	return result, eligibility, nil
}

func (m *UpdateManager) logBlocker(level slog.Level, code, tag string) {
	if m.Log == nil {
		return
	}
	msg := "update blocked"
	if level >= slog.LevelWarn {
		msg = "[FIX] update blocked"
	}
	m.Log.Log(context.Background(), level, msg, slog.String("code", code), slog.String("tag", tag))
}

// shortCommit is the 12-character prefix used in logs.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func fingerprint(e UpdateEligibility, result domain.UpdateCheck) string {
	i, c := e.Installation, e.Checkout
	value := strings.Join([]string{i.Root, i.SourceKind, i.Owner, i.Repo, i.RequestedRef, i.ResolvedCommit,
		i.ManifestVersion, i.BinaryVersion, i.RunningVersion, c.Commit, c.TargetCommit, c.Origin,
		result.Release.AssetURL, result.Release.ChecksumsURL, result.Checksum, result.Commit, result.Signer}, "\n")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
