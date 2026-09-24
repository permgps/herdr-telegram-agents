// Package github reads published plugin releases from GitHub's REST API.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	defaultURL  = "https://api.github.com/repos/permgps/herdr-telegram-agents/releases"
	maxPages    = 20
	pageSize    = 100
	maxResponse = 4 << 20
)

// Source has injectable transport and endpoint for offline tests.
type Source struct {
	Client       *http.Client
	URL          string
	ManifestBase string
	OS           string
	Arch         string
	Log          *slog.Logger
}

var checksumPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var releaseManifestVersion = regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"\s*$`)
var releaseMinHerdr = regexp.MustCompile(`(?m)^min_herdr_version\s*=\s*"([^"]+)"\s*$`)

func NewSource(client *http.Client, log *slog.Logger) *Source {
	return &Source{Client: client, URL: defaultURL, OS: runtime.GOOS, Arch: runtime.GOARCH, Log: log}
}

type apiRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name  string `json:"name"`
		URL   string `json:"browser_download_url"`
		State string `json:"state"`
	} `json:"assets"`
}

// Latest scans the entire bounded releases list. A partial scan is an error,
// even when a candidate was already found, because a later page can contain
// a higher semantic version.
func (s *Source) Latest(ctx context.Context, installed domain.Version) (domain.Release, bool, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	log := s.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	base := s.URL
	if base == "" {
		base = defaultURL
	}
	asset := s.assetName()

	next, err := url.Parse(base)
	if err != nil {
		return domain.Release{}, false, fmt.Errorf("release URL: %w", err)
	}
	q := next.Query()
	q.Set("per_page", "100")
	q.Set("page", "1")
	next.RawQuery = q.Encode()
	seenURLs := map[string]bool{}
	seenVersions := map[string]bool{}
	var best domain.Release
	bestFound := false
	for page := 1; next != nil; page++ {
		if page > maxPages {
			return domain.Release{}, false, fmt.Errorf("release scan exceeds %d pages", maxPages)
		}
		if seenURLs[next.String()] {
			return domain.Release{}, false, fmt.Errorf("release pagination cycle")
		}
		seenURLs[next.String()] = true
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, next.String(), nil)
		if err != nil {
			cancel()
			return domain.Release{}, false, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "herdr-telegram-agents")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			log.Warn("release request failed", slog.Int("page", page), slog.String("class", "network"))
			return domain.Release{}, false, fmt.Errorf("release request page %d: %w", page, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			class := "http"
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
				class = "rate_limit"
			}
			log.Warn("release request failed", slog.Int("page", page), slog.String("class", class), slog.Int("status", resp.StatusCode))
			return domain.Release{}, false, fmt.Errorf("release request page %d: HTTP %d (%s)", page, resp.StatusCode, class)
		}
		var releases []apiRelease
		decErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponse+1)).Decode(&releases)
		link := resp.Header.Get("Link")
		_ = resp.Body.Close()
		cancel()
		if decErr != nil {
			log.Warn("release response invalid", slog.Int("page", page))
			return domain.Release{}, false, fmt.Errorf("decode releases page %d: %w", page, decErr)
		}
		if len(releases) > pageSize {
			return domain.Release{}, false, fmt.Errorf("release page %d exceeds limit", page)
		}
		for _, rel := range releases {
			if rel.Draft {
				continue
			}
			v, err := domain.ParseVersion(rel.Tag)
			if err != nil {
				log.Warn("release tag ignored", slog.String("tag", rel.Tag), slog.String("class", "invalid_version"))
				continue
			}
			key := fmt.Sprintf("%d.%d.%d-%s", v.Major, v.Minor, v.Patch, strings.Join(v.Pre, "."))
			if seenVersions[key] {
				log.Warn("duplicate release version", slog.String("tag", rel.Tag))
				continue
			}
			seenVersions[key] = true
			if v.Compare(installed) <= 0 || bestFound && v.Compare(best.Version) <= 0 {
				continue
			}
			var binaryURL, checksumsURL string
			for _, a := range rel.Assets {
				if a.State != "uploaded" || a.URL == "" {
					continue
				}
				if a.Name == asset {
					binaryURL = a.URL
				}
				if a.Name == "checksums.txt" {
					checksumsURL = a.URL
				}
			}
			// An incomplete highest release is a blocker, not a reason to
			// silently choose an older installable release.
			best = domain.Release{Tag: rel.Tag, Version: v, AssetURL: binaryURL, ChecksumsURL: checksumsURL}
			bestFound = true
		}
		log.Info("release page checked", slog.Int("page", page), slog.Int("count", len(releases)))
		if link == "" {
			if len(releases) == pageSize {
				// Probe the next page even if a proxy stripped Link. Only a
				// short or empty page proves completion.
				q := next.Query()
				q.Set("page", fmt.Sprint(page+1))
				next.RawQuery = q.Encode()
			} else {
				next = nil
			}
		} else {
			next, err = nextLink(next, link)
			if err != nil {
				return domain.Release{}, false, err
			}
		}
	}
	if bestFound {
		log.Info("release selected", slog.String("tag", best.Tag), slog.Bool("assets_ready", best.AssetURL != "" && best.ChecksumsURL != ""))
	} else {
		log.Info("release scan complete", slog.String("outcome", "up_to_date"))
	}
	return best, bestFound, nil
}

func (s *Source) assetName() string {
	osName, arch := s.OS, s.Arch
	if osName == "" {
		osName = runtime.GOOS
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	if osName == "windows" {
		arch = "amd64"
	}
	asset := "herdr-tg_" + osName + "_" + arch
	if osName == "windows" {
		asset += ".exe"
	}
	return asset
}

// Checksum verifies that the selected release carries an exact SHA-256 entry
// for the host binary. It returns the digest for a later staged download.
func (s *Source) Checksum(ctx context.Context, release domain.Release) (string, error) {
	if release.AssetURL == "" || release.ChecksumsURL == "" {
		return "", fmt.Errorf("release %s is missing the host binary or checksums.txt", release.Tag)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, release.ChecksumsURL, nil)
	if err != nil {
		return "", fmt.Errorf("checksums request: %w", err)
	}
	req.Header.Set("User-Agent", "herdr-telegram-agents")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch checksums: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("checksums file too large")
	}
	var digest string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != s.assetName() {
			continue
		}
		if digest != "" {
			return "", fmt.Errorf("duplicate checksum for %s", s.assetName())
		}
		if !checksumPattern.MatchString(fields[0]) {
			return "", fmt.Errorf("invalid checksum for %s", s.assetName())
		}
		digest = strings.ToLower(fields[0])
	}
	if digest == "" {
		return "", fmt.Errorf("checksum missing for %s", s.assetName())
	}
	return digest, nil
}

// Manifest reads the manifest at the exact release tag, not the default
// branch, so compatibility and version checks cannot drift between presses.
func (s *Source) Manifest(ctx context.Context, release domain.Release) (domain.ReleaseManifest, error) {
	if _, err := domain.ParseVersion(release.Tag); err != nil {
		return domain.ReleaseManifest{}, err
	}
	base := s.ManifestBase
	if base == "" {
		base = "https://raw.githubusercontent.com/permgps/herdr-telegram-agents"
	}
	address := strings.TrimRight(base, "/") + "/" + url.PathEscape(release.Tag) + "/herdr-plugin.toml"
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, address, nil)
	if err != nil {
		return domain.ReleaseManifest{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return domain.ReleaseManifest{}, fmt.Errorf("fetch release manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return domain.ReleaseManifest{}, fmt.Errorf("fetch release manifest: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return domain.ReleaseManifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	if len(data) > 1<<20 {
		return domain.ReleaseManifest{}, fmt.Errorf("release manifest too large")
	}
	versionMatch, minMatch := releaseManifestVersion.FindSubmatch(data), releaseMinHerdr.FindSubmatch(data)
	if versionMatch == nil || minMatch == nil {
		return domain.ReleaseManifest{}, fmt.Errorf("release manifest missing version or minimum Herdr version")
	}
	v, err := domain.ParseVersion(string(versionMatch[1]))
	if err != nil || v.Compare(release.Version) != 0 {
		return domain.ReleaseManifest{}, fmt.Errorf("release tag and manifest version differ")
	}
	if _, err := domain.ParseVersion(string(minMatch[1])); err != nil {
		return domain.ReleaseManifest{}, fmt.Errorf("release requires invalid Herdr version")
	}
	return domain.ReleaseManifest{Version: string(versionMatch[1]), MinHerdrVersion: string(minMatch[1])}, nil
}

func nextLink(base *url.URL, header string) (*url.URL, error) {
	for _, part := range strings.Split(header, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		left := strings.IndexByte(part, '<')
		right := strings.IndexByte(part, '>')
		if left < 0 || right <= left {
			return nil, fmt.Errorf("malformed release pagination link")
		}
		ref, err := url.Parse(part[left+1 : right])
		if err != nil {
			return nil, fmt.Errorf("release pagination link: %w", err)
		}
		next := base.ResolveReference(ref)
		if next.Host != base.Host || next.Scheme != base.Scheme || next.Path != base.Path {
			return nil, fmt.Errorf("release pagination changed endpoint")
		}
		return next, nil
	}
	return nil, nil
}
