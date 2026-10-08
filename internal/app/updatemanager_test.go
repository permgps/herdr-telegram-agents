package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type fakeReleaseSource struct {
	release  domain.Release
	checksum string
	commit   string
	stmtErr  error
	calls    int
}

func (f *fakeReleaseSource) Latest(_ context.Context, installed domain.Version) (domain.Release, bool, error) {
	f.calls++
	return f.release, f.release.Version.Compare(installed) > 0, nil
}
func (f *fakeReleaseSource) Statement(_ context.Context, r domain.Release) (domain.ReleaseStatement, error) {
	if f.stmtErr != nil {
		return domain.ReleaseStatement{}, f.stmtErr
	}
	commit := f.commit
	if commit == "" {
		commit = "1111111111111111111111111111111111111111"
	}
	return domain.ReleaseStatement{Tag: r.Tag, Commit: commit, Checksum: f.checksum, Signer: "SHA256:test"}, nil
}
func (f *fakeReleaseSource) Manifest(context.Context, domain.Release) (domain.ReleaseManifest, error) {
	return domain.ReleaseManifest{Version: "1.2.0", MinHerdrVersion: "0.7.5"}, nil
}

type fakeHerdrProber struct{}

func (fakeHerdrProber) Ping(context.Context) (domain.HerdrInfo, error) {
	return domain.HerdrInfo{Version: "0.9.1"}, nil
}

func TestUpdateIntentBindingReplayExpiryAndDrift(t *testing.T) {
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	v, _ := domain.ParseVersion("1.2.0")
	source := &fakeReleaseSource{release: domain.Release{Tag: "v1.2.0", Version: v, AssetURL: "asset", ChecksumsURL: "sums"}, checksum: "digest"}
	managed := t.TempDir()
	root := filepath.Join(managed, "plugin")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	preflight := &UpdatePreflight{Installation: installReader{domain.PluginInstallation{Root: root, ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", SourceKind: "github", Owner: "permgps", Repo: "herdr-telegram-agents", ManagedPath: managed, ResolvedCommit: "old-commit"}}}
	m := &UpdateManager{Releases: source, Preflight: preflight, Herdr: fakeHerdrProber{}, Now: func() time.Time { return now }}
	check, err := m.Check(context.Background(), 42, 100, 7)
	if err != nil || check.IntentID == "" {
		t.Fatalf("check=%+v err=%v", check, err)
	}
	for _, binding := range [][3]int64{{41, 100, 7}, {42, 101, 7}, {42, 100, 8}} {
		_, err := m.Consume(context.Background(), check.IntentID, binding[0], int(binding[1]), binding[2])
		if !errors.Is(err, ErrUpdateIntent) {
			t.Fatalf("binding=%v err=%v", binding, err)
		}
		check, err = m.Check(context.Background(), 42, 100, 7)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Consume(context.Background(), check.IntentID, 42, 100, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Consume(context.Background(), check.IntentID, 42, 100, 7); !errors.Is(err, ErrUpdateIntent) {
		t.Fatalf("replay err=%v", err)
	}
	check, _ = m.Check(context.Background(), 42, 100, 7)
	source.checksum = "changed"
	if _, err := m.Consume(context.Background(), check.IntentID, 42, 100, 7); !errors.Is(err, ErrUpdateIntent) {
		t.Fatalf("checksum drift err=%v", err)
	}
	check, _ = m.Check(context.Background(), 42, 100, 7)
	now = now.Add(6 * time.Minute)
	if _, err := m.Consume(context.Background(), check.IntentID, 42, 100, 7); !errors.Is(err, ErrUpdateIntent) {
		t.Fatalf("expiry err=%v", err)
	}
}

// TestUpdateCheckSignatureBlockers: an unsigned or badly signed release is a
// blocker (never "up to date", never an error that hides the release), and a
// linked checkout whose tag moved off the signed commit is refused.
func TestUpdateCheckSignatureBlockers(t *testing.T) {
	v, _ := domain.ParseVersion("1.2.0")
	managed := t.TempDir()
	root := filepath.Join(managed, "plugin")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	signed := "2222222222222222222222222222222222222222"
	github := domain.PluginInstallation{Root: root, ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", SourceKind: "github", Owner: "permgps", Repo: "herdr-telegram-agents", ManagedPath: managed, ResolvedCommit: "old-commit"}
	local := github
	local.SourceKind = "local"
	checkout := domain.CheckoutState{Branch: "main", Origin: "https://github.com/permgps/herdr-telegram-agents.git", Commit: "old", TargetCommit: signed, FastForward: true}
	for _, tc := range []struct {
		name    string
		install domain.PluginInstallation
		target  string
		err     error
		want    string
	}{
		{"signed managed", github, signed, nil, ""},
		{"signed local", local, signed, nil, ""},
		{"unsigned", github, signed, fmt.Errorf("release v1.2.0: %w", domain.ErrReleaseUnsigned), "unsigned"},
		{"bad signature", github, signed, fmt.Errorf("release v1.2.0: %w: x", domain.ErrReleaseSignature), "bad_signature"},
		{"moved tag", local, "3333333333333333333333333333333333333333", nil, "commit_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &fakeReleaseSource{release: domain.Release{Tag: "v1.2.0", Version: v, AssetURL: "asset", ChecksumsURL: "sums"}, checksum: "digest", commit: signed, stmtErr: tc.err}
			c := checkout
			c.TargetCommit = tc.target
			preflight := &UpdatePreflight{Installation: installReader{tc.install}, Checkout: checkoutReader{c}}
			m := &UpdateManager{Releases: source, Preflight: preflight, Herdr: fakeHerdrProber{}}
			check, err := m.Check(context.Background(), 42, 100, 7)
			if err != nil || !check.Available || check.BlockerCode != tc.want {
				t.Fatalf("check=%+v err=%v", check, err)
			}
			if (check.IntentID != "") != (tc.want == "") {
				t.Fatalf("intent=%q with blocker %q", check.IntentID, check.BlockerCode)
			}
			if tc.want == "" && (check.Commit != signed || check.Signer == "") {
				t.Fatalf("statement not carried: %+v", check)
			}
		})
	}
}

// TestUpdateIntentRejectsCommitDrift: the signed commit is part of the
// fingerprint, so a re-signed release between the presses voids the intent.
func TestUpdateIntentRejectsCommitDrift(t *testing.T) {
	v, _ := domain.ParseVersion("1.2.0")
	managed := t.TempDir()
	root := filepath.Join(managed, "plugin")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := &fakeReleaseSource{release: domain.Release{Tag: "v1.2.0", Version: v, AssetURL: "asset", ChecksumsURL: "sums"}, checksum: "digest"}
	preflight := &UpdatePreflight{Installation: installReader{domain.PluginInstallation{Root: root, ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", SourceKind: "github", Owner: "permgps", Repo: "herdr-telegram-agents", ManagedPath: managed, ResolvedCommit: "old-commit"}}}
	m := &UpdateManager{Releases: source, Preflight: preflight, Herdr: fakeHerdrProber{}}
	check, err := m.Check(context.Background(), 42, 100, 7)
	if err != nil || check.IntentID == "" {
		t.Fatalf("check=%+v err=%v", check, err)
	}
	source.commit = "4444444444444444444444444444444444444444"
	if _, err := m.Consume(context.Background(), check.IntentID, 42, 100, 7); !errors.Is(err, ErrUpdateIntent) {
		t.Fatalf("commit drift err=%v", err)
	}
}
