package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestPanelUpdateCheckAuthorizationAndDuplicatePresses(t *testing.T) {
	f := newBridgeFixture(t)
	managed := t.TempDir()
	root := filepath.Join(managed, "plugin")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	v, _ := domain.ParseVersion("1.2.0")
	source := &fakeReleaseSource{release: domain.Release{Tag: "v1.2.0", Version: v, AssetURL: "asset", ChecksumsURL: "sums"}, checksum: "digest"}
	manager := &UpdateManager{Releases: source, Preflight: &UpdatePreflight{Installation: installReader{domain.PluginInstallation{Root: root, ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", SourceKind: "github", Owner: "permgps", Repo: "herdr-telegram-agents", ManagedPath: managed, ResolvedCommit: "old"}}}, Herdr: fakeHerdrProber{}, Now: f.clock.Now}
	p := f.in.panel
	p.chatID, p.operators = f.cfg.ChatID, []int64{1}
	p.updates = manager
	p.jobs = &memoryUpdateStore{}
	launches := 0
	p.launch = func(context.Context, string) (int, error) { launches++; return 22, nil }
	p.running = func() bool { return false }
	var pending []func(context.Context) any
	p.async = func(run func(context.Context) any) { pending = append(pending, run) }
	if err := p.Open(f.ctx, domain.GeneralCommand{MessageID: 10, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	id := p.lastID
	unauthorized := domain.ButtonPressed{CallbackID: "unauthorized", ThreadID: 0, MessageID: id, FromID: 2, Data: updateCheckData}
	if err := p.Press(f.ctx, unauthorized); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || source.calls != 0 {
		t.Fatal("unauthorized check ran")
	}
	press := domain.ButtonPressed{CallbackID: "check", ThreadID: 0, MessageID: id, FromID: 1, Data: updateCheckData}
	if err := p.Press(f.ctx, press); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || source.calls != 0 {
		t.Fatal("check blocked callback acknowledgement")
	}
	result := pending[0](f.ctx).(updateCheckResult)
	if err := p.checkFinished(f.ctx, result); err != nil {
		t.Fatal(err)
	}
	var updateData string
	for _, b := range f.tg.Buttons(id) {
		if b.Text == "Update" {
			updateData = b.Data
		}
	}
	if !strings.HasPrefix(updateData, updateInstallPrefix) {
		t.Fatalf("buttons=%v", f.tg.Buttons(id))
	}
	update := domain.ButtonPressed{CallbackID: "update", ThreadID: 0, MessageID: id, FromID: 1, Data: updateData}
	if err := p.Press(f.ctx, update); err != nil {
		t.Fatal(err)
	}
	if err := p.Press(f.ctx, update); err != nil {
		t.Fatal(err)
	}
	for _, run := range pending[1:] {
		r := run(f.ctx).(updateStartResult)
		if err := p.startFinished(f.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if launches != 1 || !strings.Contains(f.tg.Text(id), "Updating to v1.2.0") {
		t.Fatalf("launches=%d text=%s", launches, f.tg.Text(id))
	}
}

func TestPanelIgnoresCheckAfterReplacement(t *testing.T) {
	f := newBridgeFixture(t)
	p := f.in.panel
	p.chatID, p.operators = f.cfg.ChatID, []int64{1}
	p.updates = &UpdateManager{}
	var pending func(context.Context) any
	p.async = func(run func(context.Context) any) { pending = run }
	if err := p.Open(f.ctx, domain.GeneralCommand{MessageID: 10, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	old := p.lastID
	if err := p.Press(f.ctx, domain.ButtonPressed{CallbackID: "check", MessageID: old, FromID: 1, Data: updateCheckData}); err != nil {
		t.Fatal(err)
	}
	if pending == nil {
		t.Fatal("check not scheduled")
	}
	if err := p.Open(f.ctx, domain.GeneralCommand{MessageID: 11, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	if err := p.checkFinished(f.ctx, updateCheckResult{panelID: old, check: domain.UpdateCheck{Available: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.tg.Text(p.lastID), "New release") {
		t.Fatal("stale result replaced new panel")
	}
}

func TestPanelRestoresPersistedUpdateProgress(t *testing.T) {
	f := newBridgeFixture(t)
	p := f.in.panel
	if err := p.Open(f.ctx, domain.GeneralCommand{MessageID: 10, FromID: 1, Text: "/options"}); err != nil {
		t.Fatal(err)
	}
	id := p.lastID
	p.lastID = 0 // models a fresh daemon process
	if err := p.restoreUpdate(f.ctx, domain.UpdateJob{ID: "job", Phase: "installed", TargetTag: "v1.2.0", PanelMessageID: id}); err != nil {
		t.Fatal(err)
	}
	if p.lastID != id || !strings.Contains(f.tg.Text(id), "Phase: installed") || len(f.tg.Buttons(id)) != 0 {
		t.Fatalf("last=%d text=%q buttons=%v", p.lastID, f.tg.Text(id), f.tg.Buttons(id))
	}
}
