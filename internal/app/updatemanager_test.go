package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type fakeReleaseSource struct {
	release  domain.Release
	checksum string
	calls    int
}

func (f *fakeReleaseSource) Latest(_ context.Context, installed domain.Version) (domain.Release, bool, error) {
	f.calls++
	return f.release, f.release.Version.Compare(installed) > 0, nil
}
func (f *fakeReleaseSource) Checksum(context.Context, domain.Release) (string, error) {
	return f.checksum, nil
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
