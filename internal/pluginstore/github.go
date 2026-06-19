package pluginstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpfetch"
)

const userAgent = "CLIProxyAPI"

// HTTPDoer abstracts the HTTP client used to execute requests.
type HTTPDoer = httpfetch.Doer

type Client struct {
	HTTPClient  HTTPDoer
	RegistryURL string
	UserAgent   string
}

type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (c Client) FetchRegistry(ctx context.Context) (Registry, error) {
	registryURL := strings.TrimSpace(c.RegistryURL)
	if registryURL == "" {
		registryURL = DefaultRegistryURL
	}
	data, errDownload := c.get(ctx, registryURL, "application/json")
	if errDownload != nil {
		return Registry{}, errDownload
	}
	registry, errParse := ParseRegistry(data)
	if errParse != nil {
		return Registry{}, errParse
	}
	return registry, nil
}

// FetchLatestRelease returns the latest published release of the plugin's
// GitHub repository, mirroring the WebUI panel update check.
func (c Client) FetchLatestRelease(ctx context.Context, plugin Plugin) (Release, error) {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return Release{}, errRepository
	}
	releaseURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	data, errDownload := c.get(ctx, releaseURL, "application/vnd.github+json")
	if errDownload != nil {
		return Release{}, errDownload
	}
	var release Release
	if errDecode := json.Unmarshal(data, &release); errDecode != nil {
		return Release{}, fmt.Errorf("decode release: %w", errDecode)
	}
	return release, nil
}

func (c Client) fetchLatestReleaseFromWeb(ctx context.Context, plugin Plugin, goos, goarch string) (Release, error) {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return Release{}, errRepository
	}
	latestURL := fmt.Sprintf(
		"https://github.com/%s/%s/releases/latest",
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if errRequest != nil {
		return Release{}, errRequest
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", c.userAgent())
	resp, errDo := c.httpClient().Do(req)
	if errDo != nil {
		return Release{}, errDo
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Release{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	tag := releaseTagFromURL(resp.Header.Get("Location"))
	if tag == "" && resp.Request != nil && resp.Request.URL != nil {
		tag = releaseTagFromURL(resp.Request.URL.String())
	}
	if tag == "" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		tag = releaseTagFromHTML(string(body))
	}
	if tag == "" {
		return Release{}, fmt.Errorf("latest release tag not found")
	}
	return releaseForPluginVersion(plugin, tag, goos, goarch)
}

func releaseTagFromURL(rawURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(rawURL))
	if errParse != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "releases" || parts[i+1] != "tag" {
			continue
		}
		tag, errUnescape := url.PathUnescape(parts[i+2])
		if errUnescape != nil {
			return parts[i+2]
		}
		return tag
	}
	return ""
}

func releaseTagFromHTML(body string) string {
	marker := "/releases/tag/"
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	start := index + len(marker)
	end := start
	for end < len(body) {
		switch body[end] {
		case '"', '\'', '<', '>', '?', '#':
			goto found
		}
		end++
	}
found:
	tag, errUnescape := url.PathUnescape(strings.TrimSpace(body[start:end]))
	if errUnescape != nil {
		return strings.TrimSpace(body[start:end])
	}
	return tag
}

// ReleaseVersion derives the plugin version from the release tag, stripping a
// leading "v"/"V" and validating the result.
func ReleaseVersion(release Release) (string, error) {
	version := normalizeVersion(release.TagName)
	if !validPluginVersion(version) {
		return "", fmt.Errorf("invalid release tag %q", release.TagName)
	}
	return version, nil
}

func (c Client) DownloadAsset(ctx context.Context, asset ReleaseAsset) ([]byte, error) {
	if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
		return nil, fmt.Errorf("asset %q missing browser_download_url", asset.Name)
	}
	return c.get(ctx, asset.BrowserDownloadURL, "application/octet-stream")
}

func (c Client) get(ctx context.Context, requestURL string, accept string) ([]byte, error) {
	headers := map[string]string{
		"Accept":     accept,
		"User-Agent": c.userAgent(),
	}
	if token := gitHubAPIToken(requestURL); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return httpfetch.GetBytes(ctx, c.httpClient(), requestURL, headers, 0)
}

// gitHubAPIToken returns the optional GitHub token for GitHub API requests to
// raise the unauthenticated rate limit, mirroring the management asset updater.
func gitHubAPIToken(requestURL string) string {
	parsed, errParse := url.Parse(requestURL)
	if errParse != nil || !strings.EqualFold(parsed.Host, "api.github.com") {
		return ""
	}
	gitURL := strings.ToLower(strings.TrimSpace(os.Getenv("GITSTORE_GIT_URL")))
	if !strings.Contains(gitURL, "github.com") {
		return ""
	}
	return strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN"))
}

func (c Client) httpClient() HTTPDoer {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c Client) userAgent() string {
	if strings.TrimSpace(c.UserAgent) != "" {
		return strings.TrimSpace(c.UserAgent)
	}
	return userAgent
}

func SelectReleaseAssets(release Release, id, version, goos, goarch string) (ReleaseAsset, ReleaseAsset, error) {
	archiveName := ArchiveName(id, version, goos, goarch)
	var archiveAsset ReleaseAsset
	var checksumAsset ReleaseAsset
	for _, asset := range release.Assets {
		switch strings.TrimSpace(asset.Name) {
		case archiveName:
			archiveAsset = asset
		case "checksums.txt":
			checksumAsset = asset
		}
	}
	if strings.TrimSpace(archiveAsset.Name) == "" {
		return ReleaseAsset{}, ReleaseAsset{}, fmt.Errorf("release asset %s not found", archiveName)
	}
	if strings.TrimSpace(checksumAsset.Name) == "" {
		return ReleaseAsset{}, ReleaseAsset{}, fmt.Errorf("release asset checksums.txt not found")
	}
	return archiveAsset, checksumAsset, nil
}

func ArchiveName(id, version, goos, goarch string) string {
	return fmt.Sprintf(
		"%s_%s_%s_%s.zip",
		strings.TrimSpace(id),
		strings.TrimSpace(version),
		strings.TrimSpace(goos),
		strings.TrimSpace(goarch),
	)
}
