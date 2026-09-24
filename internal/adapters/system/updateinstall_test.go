package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
				RunCommand: func(_ context.Context, _ string, binName string, args ...string) error {
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
	i := &UpdateInstaller{HerdrBin: "herdr", OS: "windows", RunCommand: func(_ context.Context, _ string, bin string, args ...string) error {
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
