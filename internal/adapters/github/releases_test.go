package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestLatestSelectsHighestPublishedVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=2&per_page=100>; rel="next"`, "http://"+r.Host+r.URL.Path))
			fmt.Fprint(w, `[{"tag_name":"v1.1.0","assets":[]},{"tag_name":"v2.0.0","draft":true}]`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v1.2.0-rc.1","prerelease":true,"assets":[{"name":"herdr-tg_linux_amd64","state":"uploaded","browser_download_url":"https://example.com/bin"},{"name":"checksums.txt","state":"uploaded","browser_download_url":"https://example.com/sums"}]},{"tag_name":"bad"}]`)
	}))
	defer server.Close()
	s := &Source{Client: server.Client(), URL: server.URL + "/releases", OS: "linux", Arch: "amd64"}
	installed, _ := domain.ParseVersion("1.0.0")
	rel, found, err := s.Latest(context.Background(), installed)
	if err != nil || !found || rel.Tag != "v1.2.0-rc.1" || rel.AssetURL == "" || rel.ChecksumsURL == "" {
		t.Fatalf("release=%+v found=%v err=%v", rel, found, err)
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			s := &Source{Client: server.Client(), OS: "linux", Arch: "amd64"}
			got, err := s.Checksum(context.Background(), domain.Release{Tag: "v1.2.0", AssetURL: "https://example.com/bin", ChecksumsURL: server.URL})
			if (err == nil) != tc.ok {
				t.Fatalf("digest=%q err=%v", got, err)
			}
		})
	}
}
