//go:build !windows

package system

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// installRelease is a fake release served to scripts/install.sh.
type installRelease struct {
	files map[string][]byte
}

// installFixture copies install.sh and scripts/signing into a temp plugin
// root at version 9.9.9, trusting a fresh test key instead of the release key.
type installFixture struct {
	root  string
	asset string
	bin   []byte
	priv  ed25519.PrivateKey
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	repo := filepath.Join("..", "..", "..")
	if !sshKeygenVerifies(t, repo) {
		t.Skip("ssh-keygen cannot verify SSHSIG on this host")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts", "signing"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install.sh", "signing/selftest.txt", "signing/selftest.txt.sig", "signing/selftest_signers"} {
		data, err := os.ReadFile(filepath.Join(repo, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "scripts", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signers := "herdr-tg-release namespaces=\"herdr-tg-release\" " + testkit.AuthorizedKey(pub) + "\n"
	write(t, filepath.Join(root, "scripts", "signing", "allowed_signers"), signers)
	write(t, filepath.Join(root, "herdr-plugin.toml"), "version = \"9.9.9\"\n")
	return &installFixture{
		root:  root,
		asset: "herdr-tg_" + runtime.GOOS + "_" + runtime.GOARCH,
		bin:   []byte("#!/bin/sh\necho 'herdr-tg 9.9.9 (fixture)'\n"),
		priv:  priv,
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sshKeygenVerifies(t *testing.T, repo string) bool {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return false
	}
	dir := filepath.Join(repo, "scripts", "signing")
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", filepath.Join(dir, "selftest_signers"), "-I", "herdr-tg-selftest",
		"-n", "herdr-tg-selftest", "-s", filepath.Join(dir, "selftest.txt.sig"))
	in, err := os.Open(filepath.Join(dir, "selftest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	cmd.Stdin = in
	return cmd.Run() == nil
}

func (f *installFixture) digest() string {
	sum := sha256.Sum256(f.bin)
	return hex.EncodeToString(sum[:])
}

// release builds checksums.txt, release.txt and its signature for the
// fixture's binary; the caller may edit the files before serving.
func (f *installFixture) release() *installRelease {
	sums := f.digest() + "  " + f.asset + "\n" + strings.Repeat("e", 64) + "  herdr-tg_other\n"
	statement := "tag v9.9.9\ncommit " + strings.Repeat("c", 40) + "\n" + sums
	return &installRelease{files: map[string][]byte{
		f.asset:           f.bin,
		"checksums.txt":   []byte(sums),
		"release.txt":     []byte(statement),
		"release.txt.sig": testkit.SignSSH(f.priv, "herdr-tg-release", []byte(statement)),
	}}
}

// run serves rel and runs install.sh with a minimal environment; path
// replaces PATH when set.
func (f *installFixture) run(t *testing.T, rel *installRelease, approved, path string) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := rel.files[strings.TrimPrefix(r.URL.Path, "/v9.9.9/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer server.Close()
	if path == "" {
		path = os.Getenv("PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(f.root, "scripts", "install.sh"))
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "HERDR_TG_BASE_URL=" + server.URL + "/v9.9.9", "HERDR_TG_ALLOW_INSECURE_BASE=1"}
	if approved != "" {
		cmd.Env = append(cmd.Env, "HERDR_TG_EXPECTED_SHA256="+approved)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *installFixture) receipt(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "bin", "install-receipt"))
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	return string(data)
}

// pathWithout links every tool install.sh uses except ssh-keygen into a
// fresh directory, so the script sees a host without OpenSSH.
func pathWithout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"basename", "cat", "chmod", "curl", "cut", "dirname", "grep", "head", "mkdir", "mktemp", "mv", "rm", "sed", "sh", "sha256sum", "shasum", "tr", "uname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestInstallScriptSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("runs install.sh eight times")
	}
	for _, tc := range []struct {
		name      string
		edit      func(*installFixture, *installRelease)
		approved  func(*installFixture) string
		noKeygen  bool
		fails     string
		signature string
		ran       bool
	}{
		{name: "signed", signature: "verified", ran: true},
		{name: "bad signature", edit: func(f *installFixture, r *installRelease) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			r.files["release.txt.sig"] = testkit.SignSSH(other, "herdr-tg-release", r.files["release.txt"])
		}, fails: "release signature does not verify"},
		{name: "unsigned", edit: func(_ *installFixture, r *installRelease) {
			delete(r.files, "release.txt")
			delete(r.files, "release.txt.sig")
		}, signature: "absent"},
		{name: "unsigned with approved checksum", edit: func(_ *installFixture, r *installRelease) {
			delete(r.files, "release.txt.sig")
		}, approved: (*installFixture).digest, signature: "absent", ran: true},
		{name: "approved mismatch", approved: func(*installFixture) string { return strings.Repeat("0", 64) }, fails: "differs from the approved checksum"},
		{name: "signed digest differs from checksums", edit: func(f *installFixture, r *installRelease) {
			statement := "tag v9.9.9\ncommit " + strings.Repeat("c", 40) + "\n" + strings.Repeat("f", 64) + "  " + f.asset + "\n"
			r.files["release.txt"] = []byte(statement)
			r.files["release.txt.sig"] = testkit.SignSSH(f.priv, "herdr-tg-release", []byte(statement))
		}, fails: "differs from checksums.txt"},
		{name: "statement for another tag", edit: func(f *installFixture, r *installRelease) {
			statement := strings.Replace(string(r.files["release.txt"]), "v9.9.9", "v9.9.8", 1)
			r.files["release.txt"] = []byte(statement)
			r.files["release.txt.sig"] = testkit.SignSSH(f.priv, "herdr-tg-release", []byte(statement))
		}, fails: "is not for v9.9.9"},
		{name: "no ssh-keygen", noKeygen: true, signature: "unverifiable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newInstallFixture(t)
			rel := f.release()
			if tc.edit != nil {
				tc.edit(f, rel)
			}
			approved, path := "", ""
			if tc.approved != nil {
				approved = tc.approved(f)
			}
			if tc.noKeygen {
				path = pathWithout(t)
			}
			out, err := f.run(t, rel, approved, path)
			if tc.fails != "" {
				if err == nil || !strings.Contains(out, tc.fails) {
					t.Fatalf("want failure %q, got err=%v:\n%s", tc.fails, err, out)
				}
				if _, statErr := os.Stat(filepath.Join(f.root, "bin", "herdr-tg")); !os.IsNotExist(statErr) {
					t.Fatalf("binary installed despite failure:\n%s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			want := "sha256 " + f.digest() + "\napproved " + map[bool]string{true: approved, false: "none"}[approved != ""] + "\nsignature " + tc.signature + "\n"
			if got := f.receipt(t); got != want {
				t.Fatalf("receipt = %q, want %q\n%s", got, want, out)
			}
			if ran := strings.Contains(out, "herdr-tg 9.9.9 (fixture)"); ran != tc.ran {
				t.Fatalf("binary ran = %t, want %t\n%s", ran, tc.ran, out)
			}
		})
	}
}
