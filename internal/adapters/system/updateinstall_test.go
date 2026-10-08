package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestUpdateInstallerLocalBackupInstallVerifyRollback(t *testing.T) {
	for _, hostOS := range []string{"linux", "windows"} {
		t.Run(hostOS, func(t *testing.T) {
			root, stateDir := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin", updateBinaryName())
			if err := os.WriteFile(bin, []byte("old"), 0o700); err != nil {
				t.Fatal(err)
			}
			installerCommand := "sh scripts/install.sh"
			if hostOS == "windows" {
				installerCommand = "powershell -NoProfile -ExecutionPolicy Bypass -File scripts/install.ps1"
			}
			var commands []string
			i := &UpdateInstaller{StateDir: stateDir, OS: hostOS,
				Inspect: func(context.Context, string, string) (domain.CheckoutState, error) {
					return domain.CheckoutState{Branch: "main", Commit: "old-commit", TargetCommit: "new-commit", FastForward: true}, nil
				},
				RunCommand: func(_ context.Context, _ string, _ []string, binName string, args ...string) error {
					command := binName + " " + strings.Join(args, " ")
					commands = append(commands, command)
					if command == installerCommand {
						return os.WriteFile(bin, []byte("new"), 0o700)
					}
					return nil
				},
				GitOutput: func(_ context.Context, _ string, args ...string) (string, error) {
					if args[0] == "status" {
						return "", nil
					}
					return "new-commit", nil
				},
			}
			h := sha256.Sum256([]byte("new"))
			job := domain.UpdateJob{ID: "job", SourceKind: "local", SourceRoot: root, TargetTag: "v1.2.0", TargetCommit: "new-commit", TargetChecksum: hex.EncodeToString(h[:])}
			job, err := i.Backup(context.Background(), job)
			if err != nil || job.OldCommit != "old-commit" || job.OldBinaryBackup == "" {
				t.Fatalf("backup=%+v err=%v", job, err)
			}
			if err := i.Install(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if err := i.Verify(context.Background(), job, root); err != nil {
				t.Fatal(err)
			}
			if len(commands) != 2 || commands[0] != "git merge --ff-only new-commit" || commands[1] != installerCommand {
				t.Fatalf("commands=%v", commands)
			}
			if err := i.Rollback(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(bin)
			if err != nil || string(data) != "old" {
				t.Fatalf("restored=%q err=%v", data, err)
			}
		})
	}
}

func TestUpdateInstallerManagedExactRefsAndWindowsCommand(t *testing.T) {
	var commands []string
	i := &UpdateInstaller{HerdrBin: "herdr", OS: "windows", RunCommand: func(_ context.Context, _ string, _ []string, bin string, args ...string) error {
		commands = append(commands, bin+" "+strings.Join(args, " "))
		return nil
	}}
	managed := domain.UpdateJob{SourceKind: "github", TargetTag: "v1.2.0", OldCommit: "abc123"}
	if err := i.Install(context.Background(), managed); err != nil {
		t.Fatal(err)
	}
	if err := i.Rollback(context.Background(), managed); err != nil {
		t.Fatal(err)
	}
	if commands[0] != "herdr plugin install permgps/herdr-telegram-agents --ref v1.2.0 --yes" || commands[1] != "herdr plugin install permgps/herdr-telegram-agents --ref abc123 --yes" {
		t.Fatalf("commands=%v", commands)
	}
	i.StateDir = t.TempDir()
	i.Inspect = func(context.Context, string, string) (domain.CheckoutState, error) {
		return domain.CheckoutState{Branch: "main", Commit: "old", TargetCommit: "new", FastForward: true}, nil
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", updateBinaryName()), []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	job, err := i.Backup(context.Background(), domain.UpdateJob{ID: "local", SourceKind: "local", SourceRoot: root, TargetTag: "v1.2.0", TargetCommit: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if err := i.Install(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(commands[len(commands)-1], "powershell -NoProfile ") {
		t.Fatalf("windows command=%v", commands)
	}
}

// TestCopyUpdateFileSurvivesLeftoverTemp: an install killed mid-copy leaves
// its temporary file behind; the next copy (a rollback) must still work.
func TestCopyUpdateFileSurvivesLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "bin", "herdr-tg")
	if err := os.WriteFile(src, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst+".tmp", []byte("partial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyUpdateFile(src, dst); err != nil {
		t.Fatalf("copy with leftover temp: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "old" {
		t.Fatalf("dst = %q, %v", data, err)
	}
}

// TestUpdateInstallerPassesApprovedChecksum: the installer script learns
// the approved SHA-256 so it can refuse a swapped binary before running it,
// and never inherits a download override from the daemon's environment.
func TestUpdateInstallerPassesApprovedChecksum(t *testing.T) {
	t.Setenv("HERDR_TG_BASE_URL", "http://evil.example")
	sum := strings.Repeat("ab", 32)
	for _, source := range []string{"github", "local"} {
		var seen []string
		i := &UpdateInstaller{HerdrBin: "herdr", OS: "linux", RunCommand: func(_ context.Context, _ string, env []string, _ string, _ ...string) error {
			seen = append(seen, strings.Join(env, " "))
			return nil
		}}
		job := domain.UpdateJob{SourceKind: source, TargetTag: "v1.2.0", TargetCommit: "new", TargetChecksum: sum, OldCommit: "old", OldBinaryBackup: "backup", SourceRoot: t.TempDir()}
		if err := i.Install(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		last := seen[len(seen)-1]
		if !strings.Contains(last, "HERDR_TG_EXPECTED_SHA256="+sum) {
			t.Fatalf("%s: installer env %q lacks the approved checksum", source, last)
		}
	}
	env := updateEnv(nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "HERDR_TG_BASE_URL=") || strings.HasPrefix(kv, "HERDR_TG_ALLOW_INSECURE_BASE=") {
			t.Fatalf("parent override leaked: %s", kv)
		}
	}
}

func TestUpdateInstallerReceipt(t *testing.T) {
	root := t.TempDir()
	i := &UpdateInstaller{}
	if _, err := i.Receipt(context.Background(), root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing receipt err = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "bin", "install-receipt")
	body := "sha256 ABCDEF\r\napproved none\r\nsignature verified\r\nfuture value\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := i.Receipt(context.Background(), root)
	if err != nil || got != (domain.InstallReceipt{SHA256: "abcdef", Approved: "none", Signature: "verified"}) {
		t.Fatalf("receipt = %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxReceipt+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Receipt(context.Background(), root); err == nil {
		t.Fatal("oversized receipt accepted")
	}
}
