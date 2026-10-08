package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type memoryUpdateStore struct{ job domain.UpdateJob }

func (s *memoryUpdateStore) Load(context.Context) (domain.UpdateJob, error) {
	if s.job.ID == "" {
		return domain.UpdateJob{}, os.ErrNotExist
	}
	return s.job, nil
}
func (s *memoryUpdateStore) Save(_ context.Context, job domain.UpdateJob) error {
	s.job = job
	return nil
}
func (s *memoryUpdateStore) Recover(_ context.Context, _ bool) (domain.UpdateJob, error) {
	return s.job, nil
}

type memoryUpdateLock struct{ held bool }

func (l *memoryUpdateLock) Acquire() error {
	if l.held {
		return errors.New("locked")
	}
	l.held = true
	return nil
}
func (l *memoryUpdateLock) Active() bool   { return l.held }
func (l *memoryUpdateLock) Release() error { l.held = false; return nil }

// changingReader stands in for the InstallationReader, which executes the
// installed binary (`herdr-tg version`); calls records each read as "read".
type changingReader struct {
	value domain.PluginInstallation
	calls []string
}

func (r *changingReader) ReadInstallation(context.Context) (domain.PluginInstallation, error) {
	r.calls = append(r.calls, "read")
	return r.value, nil
}

type fakeUpdateInstaller struct {
	reader       *changingReader
	proc         *testkit.FakeProcess
	failInstall  bool
	failVerify   bool
	rollbacks    int
	failRollback bool
	receipt      *domain.InstallReceipt
	// commit is what Herdr reports as resolved after Install; empty keeps it.
	commit string
}

func (f *fakeUpdateInstaller) Receipt(context.Context, string) (domain.InstallReceipt, error) {
	if f.receipt == nil {
		return domain.InstallReceipt{}, os.ErrNotExist
	}
	return *f.receipt, nil
}

func (f *fakeUpdateInstaller) Backup(_ context.Context, j domain.UpdateJob) (domain.UpdateJob, error) {
	return j, nil
}
func (f *fakeUpdateInstaller) Install(_ context.Context, _ domain.UpdateJob) error {
	if f.failInstall {
		return errors.New("install failed")
	}
	f.reader.calls = append(f.reader.calls, "install")
	f.reader.value.ManifestVersion = "1.2.0"
	f.reader.value.BinaryVersion = "1.2.0"
	if f.commit != "" {
		f.reader.value.ResolvedCommit = f.commit
	}
	return nil
}
func (f *fakeUpdateInstaller) Verify(_ context.Context, _ domain.UpdateJob, root string) error {
	f.reader.calls = append(f.reader.calls, "verify "+root)
	if f.failVerify {
		return errors.New("checksum differs")
	}
	return nil
}
func (f *fakeUpdateInstaller) Rollback(_ context.Context, _ domain.UpdateJob) error {
	f.reader.calls = append(f.reader.calls, "rollback")
	f.rollbacks++
	if f.failRollback {
		return errors.New("rollback failed")
	}
	f.reader.value.ManifestVersion = "1.0.0"
	f.reader.value.BinaryVersion = "1.0.0"
	f.proc.SetStatusLine("version=1.0.0 pid=1002 herdr=ok telegram=ready")
	return nil
}

type quickClock struct{ now time.Time }

func (c *quickClock) Now() time.Time { return c.now }
func (c *quickClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func TestUpdateWorkerHandoffAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		running, fail bool
		phase         string
		rollbacks     int
	}{
		{"managed running", true, false, "succeeded", 0},
		{"stopped stays stopped", false, false, "succeeded", 0},
		{"install failure rolls back", true, true, "rolled_back", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
			proc := testkit.NewFakeProcess(clock.Now)
			if tc.running {
				if _, err := proc.Spawn(context.Background(), []string{"daemon"}); err != nil {
					t.Fatal(err)
				}
			}
			proc.SetStatusLine("version=1.2.0 pid=1002 herdr=ok telegram=ready")
			reader := &changingReader{value: domain.PluginInstallation{Root: "/new", ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", ResolvedCommit: "new-commit"}}
			installer := &fakeUpdateInstaller{reader: reader, proc: proc, failInstall: tc.fail}
			store := &memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "queued", OldVersion: "1.0.0", OldBinaryVersion: "1.0.0", TargetVersion: "1.2.0", PriorRunning: tc.running, OldCommit: "old-commit"}}
			worker := &UpdateWorker{Store: store, Lock: &memoryUpdateLock{}, Installer: installer, Reader: reader, Supervisor: NewSupervisor(proc, proc, clock, nil)}
			job, err := worker.Run(context.Background(), "job")
			if tc.fail && err == nil || !tc.fail && err != nil {
				t.Fatalf("err=%v", err)
			}
			if job.Phase != tc.phase || installer.rollbacks != tc.rollbacks {
				t.Fatalf("job=%+v rollbacks=%d", job, installer.rollbacks)
			}
			if (proc.Held() != 0) != tc.running {
				t.Fatalf("daemon state: %s", proc)
			}
		})
	}
}

func TestUpdateWorkerHealthTimeoutAndStuck(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		failInstall, failRollback bool
		want                      string
	}{
		{"health timeout rollback", false, false, "rolled_back"},
		{"rollback failure stuck", true, true, "stuck"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &quickClock{now: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)}
			proc := testkit.NewFakeProcess(clock.Now)
			if _, err := proc.Spawn(context.Background(), []string{"daemon"}); err != nil {
				t.Fatal(err)
			}
			proc.SetStatusLine("version=wrong pid=1002 herdr=ok telegram=ready")
			reader := &changingReader{value: domain.PluginInstallation{Root: "/new", ManifestVersion: "1.0.0", BinaryVersion: "1.0.0"}}
			installer := &fakeUpdateInstaller{reader: reader, proc: proc, failInstall: tc.failInstall, failRollback: tc.failRollback}
			store := &memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "queued", OldVersion: "1.0.0", OldBinaryVersion: "1.0.0", TargetVersion: "1.2.0", PriorRunning: true}}
			worker := &UpdateWorker{Store: store, Lock: &memoryUpdateLock{}, Installer: installer, Reader: reader, Supervisor: NewSupervisor(proc, proc, clock, nil), HealthTimeout: time.Second}
			job, err := worker.Run(context.Background(), "job")
			if err == nil || job.Phase != tc.want || installer.rollbacks != 1 {
				t.Fatalf("job=%+v err=%v rollbacks=%d", job, err, installer.rollbacks)
			}
		})
	}
}

// TestUpdateWorkerVerifiesBeforeExecuting: reading the installation runs the
// new binary, so its checksum is verified at the job's root first, and a bad
// checksum rolls back without the new binary ever running.
func TestUpdateWorkerVerifiesBeforeExecuting(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failVerify bool
		phase      string
		want       []string
	}{
		{"good checksum", false, "succeeded", []string{"install", "verify /src", "read", "verify /src"}},
		{"bad checksum", true, "rolled_back", []string{"install", "verify /src", "rollback", "read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
			proc := testkit.NewFakeProcess(clock.Now)
			reader := &changingReader{value: domain.PluginInstallation{Root: "/src", ManifestVersion: "1.0.0", BinaryVersion: "1.0.0"}}
			installer := &fakeUpdateInstaller{reader: reader, proc: proc, failVerify: tc.failVerify}
			store := &memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "queued", OldVersion: "1.0.0", OldBinaryVersion: "1.0.0", TargetVersion: "1.2.0", SourceRoot: "/src"}}
			worker := &UpdateWorker{Store: store, Lock: &memoryUpdateLock{}, Installer: installer, Reader: reader, Supervisor: NewSupervisor(proc, proc, clock, nil)}
			job, err := worker.Run(context.Background(), "job")
			if (err != nil) != tc.failVerify || job.Phase != tc.phase {
				t.Fatalf("job=%+v err=%v", job, err)
			}
			if strings.Join(reader.calls, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("calls = %q, want %q", reader.calls, tc.want)
			}
			if tc.failVerify && job.ErrorCode != "checksum_mismatch" {
				t.Fatalf("error code = %q", job.ErrorCode)
			}
		})
	}
}

// TestUpdateWorkerBindsManagedInstallToSignedCommit: a managed job without a
// signed commit touches nothing; a managed install that resolved another
// commit (the tag moved) rolls back; "approved none" in the receipt is only
// logged.
func TestUpdateWorkerBindsManagedInstallToSignedCommit(t *testing.T) {
	signed := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, target, resolved string
		phase, code            string
		calls                  string
	}{
		{"signed commit", signed, signed, "succeeded", "", "install,verify /src,read,verify /src"},
		{"tag moved", signed, strings.Repeat("b", 40), "rolled_back", "commit_mismatch", "install,verify /src,read,rollback,read"},
		{"no commit", "", signed, "failed", "commit_unknown", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
			proc := testkit.NewFakeProcess(clock.Now)
			reader := &changingReader{value: domain.PluginInstallation{Root: "/src", ManifestVersion: "1.0.0", BinaryVersion: "1.0.0", ResolvedCommit: "old"}}
			receipt := domain.InstallReceipt{SHA256: "digest", Approved: "none", Signature: "verified"}
			installer := &fakeUpdateInstaller{reader: reader, proc: proc, commit: tc.resolved, receipt: &receipt}
			store := &memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "queued", OldVersion: "1.0.0", OldBinaryVersion: "1.0.0", TargetVersion: "1.2.0",
				SourceKind: "github", SourceRoot: "/src", TargetCommit: tc.target, TargetChecksum: "digest", OldCommit: "old"}}
			var logs bytes.Buffer
			worker := &UpdateWorker{Store: store, Lock: &memoryUpdateLock{}, Installer: installer, Reader: reader,
				Supervisor: NewSupervisor(proc, proc, clock, nil), Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
			job, err := worker.Run(context.Background(), "job")
			if (err != nil) != (tc.code != "") || job.Phase != tc.phase || job.ErrorCode != tc.code {
				t.Fatalf("job=%+v err=%v", job, err)
			}
			if got := strings.Join(reader.calls, ","); got != tc.calls {
				t.Fatalf("calls = %q, want %q", got, tc.calls)
			}
			if tc.code != "commit_unknown" && !strings.Contains(logs.String(), "approved checksum did not reach the install script") {
				t.Fatalf("receipt warning missing:\n%s", logs.String())
			}
		})
	}
}
