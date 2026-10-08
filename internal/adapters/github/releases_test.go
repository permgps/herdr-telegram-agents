package github

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestLatestSelectsHighestPublishedVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=2&per_page=100>; rel="next"`, "http://"+r.Host+r.URL.Path))
			fmt.Fprint(w, `[{"tag_name":"v1.1.0","assets":[]},{"tag_name":"v2.0.0","draft":true}]`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v1.2.0-rc.1","prerelease":true,"assets":[{"name":"herdr-tg_linux_amd64","state":"uploaded","browser_download_url":"https://github.com/bin"},{"name":"checksums.txt","state":"uploaded","browser_download_url":"https://github.com/sums"}]},{"tag_name":"bad"}]`)
	}))
	defer server.Close()
	s := &Source{Client: server.Client(), URL: server.URL + "/releases", OS: "linux", Arch: "amd64"}
	// A stable installation is never offered a prerelease.
	installed, _ := domain.ParseVersion("1.0.0")
	rel, found, err := s.Latest(context.Background(), installed)
	if err != nil || !found || rel.Tag != "v1.1.0" {
		t.Fatalf("stable: release=%+v found=%v err=%v", rel, found, err)
	}
	// An installed prerelease opted into testing and may move to the next.
	installed, _ = domain.ParseVersion("1.0.0-rc.1")
	rel, found, err = s.Latest(context.Background(), installed)
	if err != nil || !found || rel.Tag != "v1.2.0-rc.1" || rel.AssetURL == "" || rel.ChecksumsURL == "" {
		t.Fatalf("prerelease: release=%+v found=%v err=%v", rel, found, err)
	}
}

func TestLatestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"rate limited", 403, `{}`}, {"invalid body", 200, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			s := &Source{Client: server.Client(), URL: server.URL}
			v, _ := domain.ParseVersion("1.0.0")
			if _, _, err := s.Latest(context.Background(), v); err == nil {
				t.Fatal("expected scan failure")
			}
		})
	}
}

func TestLatestRejectsCrossHostPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", `<https://example.com/steal>; rel="next"`)
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	s := &Source{Client: server.Client(), URL: server.URL}
	v, _ := domain.ParseVersion("1.0.0")
	_, _, err := s.Latest(context.Background(), v)
	if err == nil || !strings.Contains(err.Error(), "changed endpoint") {
		t.Fatalf("err=%v", err)
	}
}

func TestChecksumRequiresExactUniqueAsset(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", strings.Repeat("a", 64) + "  herdr-tg_linux_amd64\n", true},
		{"wrong asset", strings.Repeat("a", 64) + "  herdr-tg_linux_arm64\n", false},
		{"duplicate", strings.Repeat("a", 64) + "  herdr-tg_linux_amd64\n" + strings.Repeat("b", 64) + "  herdr-tg_linux_amd64\n", false},
		{"bad digest", "abc  herdr-tg_linux_amd64\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checksumFor([]byte(tc.body), "herdr-tg_linux_amd64")
			if (err == nil) != tc.ok {
				t.Fatalf("digest=%q err=%v", got, err)
			}
		})
	}
}

func allowAnyURL(*url.URL) error { return nil }

// offline fails every request, so an allowed URL is never fetched for real.
type offline struct{}

func (offline) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }

// TestReleaseURLsAllowList: asset and checksum URLs come from API JSON; only
// https on GitHub's own hosts may be fetched or handed to the installer.
func TestReleaseURLsAllowList(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://github.com/permgps/herdr-telegram-agents/releases/download/v1.2.0/checksums.txt", true},
		{"http://github.com/permgps/herdr-telegram-agents/releases/download/v1.2.0/checksums.txt", false},
		{"https://evil.example/checksums.txt", false},
		{"https://github.com.evil.example/x", false},
		{"https://user@github.com/x", false},
	} {
		s := &Source{Client: &http.Client{Transport: offline{}}, OS: "linux", Arch: "amd64"}
		_, err := s.Statement(context.Background(), domain.Release{Tag: "v1.2.0", AssetURL: tc.url, ChecksumsURL: tc.url, StatementURL: tc.url, SignatureURL: tc.url})
		rejected := err != nil && strings.Contains(err.Error(), "not allowed")
		if rejected == tc.ok {
			t.Fatalf("%s: err = %v", tc.url, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.2.0","assets":[{"name":"herdr-tg_linux_amd64","state":"uploaded","browser_download_url":"http://evil.example/bin"},{"name":"checksums.txt","state":"uploaded","browser_download_url":"https://github.com/sums"}]}]`)
	}))
	defer server.Close()
	s := &Source{Client: server.Client(), URL: server.URL, OS: "linux", Arch: "amd64"}
	installed, _ := domain.ParseVersion("1.0.0")
	rel, found, err := s.Latest(context.Background(), installed)
	if err != nil || !found || rel.AssetURL != "" {
		t.Fatalf("foreign asset URL kept: %+v %v %v", rel, found, err)
	}
}

// TestReleaseClientRedirectAllowList: GitHub redirects downloads to its
// asset hosts; any other target or scheme ends the request.
func TestReleaseClientRedirectAllowList(t *testing.T) {
	c := NewHTTPClient()
	check := func(target string, hops int) error {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		return c.CheckRedirect(req, make([]*http.Request, hops))
	}
	if err := check("https://release-assets.githubusercontent.com/x", 1); err != nil {
		t.Fatal(err)
	}
	if err := check("https://objects.githubusercontent.com/x", 1); err != nil {
		t.Fatal(err)
	}
	if check("https://evil.example/x", 1) == nil || check("http://objects.githubusercontent.com/x", 1) == nil || check("https://github.com/x", 5) == nil {
		t.Fatal("redirect allow-list not enforced")
	}
}

// signedRelease serves checksums.txt, release.txt and release.txt.sig for
// v1.2.0 from one httptest server; mutate edits the files before serving.
type signedRelease struct {
	sums, statement, sig []byte
}

func newSignedRelease(t *testing.T, priv ed25519.PrivateKey) *signedRelease {
	t.Helper()
	sums := strings.Repeat("a", 64) + "  herdr-tg_linux_amd64\n" + strings.Repeat("b", 64) + "  herdr-tg_darwin_arm64\n"
	statement := "tag v1.2.0\ncommit " + strings.Repeat("c", 40) + "\n" + sums
	return &signedRelease{sums: []byte(sums), statement: []byte(statement), sig: testkit.SignSSH(priv, releaseNamespace, []byte(statement))}
}

func (r *signedRelease) serve(t *testing.T, keys []ed25519.PublicKey) (*Source, domain.Release) {
	t.Helper()
	mux := http.NewServeMux()
	for name, body := range map[string][]byte{"/checksums.txt": r.sums, "/release.txt": r.statement, "/release.txt.sig": r.sig} {
		mux.HandleFunc(name, func(w http.ResponseWriter, _ *http.Request) { w.Write(body) })
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	s := &Source{Client: server.Client(), OS: "linux", Arch: "amd64", CheckURL: allowAnyURL, signers: keys}
	return s, domain.Release{Tag: "v1.2.0", AssetURL: server.URL + "/bin", ChecksumsURL: server.URL + "/checksums.txt",
		StatementURL: server.URL + "/release.txt", SignatureURL: server.URL + "/release.txt.sig"}
}

func TestStatementVerifiesSignedRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	s, rel := newSignedRelease(t, priv).serve(t, keys)
	got, err := s.Statement(context.Background(), rel)
	if err != nil || got.Tag != "v1.2.0" || got.Commit != strings.Repeat("c", 40) || got.Checksum != strings.Repeat("a", 64) || !strings.HasPrefix(got.Signer, "SHA256:") {
		t.Fatalf("statement = %+v, %v", got, err)
	}
}

func TestStatementFailsClosed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	resign := func(r *signedRelease) { r.sig = testkit.SignSSH(priv, releaseNamespace, r.statement) }
	for _, tc := range []struct {
		name   string
		mutate func(*signedRelease)
		unsign bool
		want   error
	}{
		{name: "unsigned", unsign: true, want: domain.ErrReleaseUnsigned},
		{name: "other key", mutate: func(r *signedRelease) { r.sig = testkit.SignSSH(otherPriv, releaseNamespace, r.statement) }, want: domain.ErrReleaseSignature},
		{name: "other namespace", mutate: func(r *signedRelease) { r.sig = testkit.SignSSH(priv, "file", r.statement) }, want: domain.ErrReleaseSignature},
		{name: "tampered statement", mutate: func(r *signedRelease) { r.statement = append(r.statement, r.statement[len(r.statement)-30:]...) }, want: domain.ErrReleaseSignature},
		{name: "other tag", mutate: func(r *signedRelease) {
			r.statement = []byte(strings.Replace(string(r.statement), "v1.2.0", "v1.3.0", 1))
			resign(r)
		}, want: domain.ErrReleaseSignature},
		{name: "duplicate asset", mutate: func(r *signedRelease) {
			r.statement = append(r.statement, []byte(strings.Repeat("a", 64)+"  herdr-tg_linux_amd64\n")...)
			resign(r)
		}, want: domain.ErrReleaseSignature},
		{name: "crlf", mutate: func(r *signedRelease) {
			r.statement = []byte(strings.ReplaceAll(string(r.statement), "\n", "\r\n"))
			resign(r)
		}, want: domain.ErrReleaseSignature},
		{name: "short commit", mutate: func(r *signedRelease) {
			r.statement = []byte(strings.Replace(string(r.statement), strings.Repeat("c", 40), "cccc", 1))
			resign(r)
		}, want: domain.ErrReleaseSignature},
		{name: "checksums differ", mutate: func(r *signedRelease) {
			r.sums = []byte(strings.Repeat("d", 64) + "  herdr-tg_linux_amd64\n")
		}, want: domain.ErrReleaseSignature},
		{name: "oversized signature", mutate: func(r *signedRelease) { r.sig = make([]byte, maxSignature+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSignedRelease(t, priv)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			s, rel := r.serve(t, keys)
			if tc.unsign {
				rel.SignatureURL = ""
			}
			got, err := s.Statement(context.Background(), rel)
			if err == nil {
				t.Fatalf("statement accepted: %+v", got)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A signed release whose statement assets sit on a foreign host is dropped by
// Latest like any other asset, so it reads as unsigned, never as signed.
func TestLatestKeepsSignatureURLsOnAllowList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.2.0","assets":[`+
			`{"name":"herdr-tg_linux_amd64","state":"uploaded","browser_download_url":"https://github.com/bin"},`+
			`{"name":"checksums.txt","state":"uploaded","browser_download_url":"https://github.com/sums"},`+
			`{"name":"release.txt","state":"uploaded","browser_download_url":"https://github.com/stmt"},`+
			`{"name":"release.txt.sig","state":"uploaded","browser_download_url":"https://evil.example/sig"}]}]`)
	}))
	defer server.Close()
	s := &Source{Client: server.Client(), URL: server.URL, OS: "linux", Arch: "amd64"}
	installed, _ := domain.ParseVersion("1.0.0")
	rel, found, err := s.Latest(context.Background(), installed)
	if err != nil || !found || rel.StatementURL != "https://github.com/stmt" || rel.SignatureURL != "" {
		t.Fatalf("release = %+v %v %v", rel, found, err)
	}
	if _, err := s.Statement(context.Background(), rel); !errors.Is(err, domain.ErrReleaseUnsigned) {
		t.Fatalf("err = %v", err)
	}
}
