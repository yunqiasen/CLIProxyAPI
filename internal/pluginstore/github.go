package pluginstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
	log "github.com/sirupsen/logrus"
)

const userAgent = "CLIProxyAPI"
const maxPluginStoreRedirects = 10

// HTTPDoer abstracts the HTTP client used to execute requests.
type HTTPDoer = httpfetch.Doer

type Client struct {
	HTTPClient HTTPDoer
	// NetworkScope distinguishes request state for different proxy/egress configurations.
	NetworkScope          string
	RateLimiter           *GitHubRateLimiter
	RegistryURL           string
	UserAgent             string
	Auth                  []AuthConfig
	ResolvedAuth          []ResolvedAuthConfig
	ResolvedAuthExpiresAt time.Time
	preparedAuth          *preparedPluginStoreAuth
}

type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	APIURL             string `json:"url"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (c Client) FetchRegistry(ctx context.Context) (Registry, error) {
	registryURL := strings.TrimSpace(c.RegistryURL)
	if registryURL == "" {
		registryURL = DefaultRegistryURL
	}
	data, errDownload := c.fetchRegistryBytes(ctx, registryURL)
	if errDownload != nil {
		return Registry{}, errDownload
	}
	registry, errParse := ParseRegistry(data)
	if errParse != nil {
		return Registry{}, errParse
	}
	return registry, nil
}

// registryFetchAttempts bounds retries for transient transport failures on the
// registry listing. GitHub raw endpoints intermittently drop the TLS handshake
// through local proxies; a single EOF must not blank the whole plugin store.
const registryFetchAttempts = 3

func (c Client) fetchRegistryBytes(ctx context.Context, registryURL string) ([]byte, error) {
	var errLast error
	for attempt := 1; attempt <= registryFetchAttempts; attempt++ {
		data, errDownload := c.get(ctx, registryURL, "application/json", RequestKindRegistry, 0)
		if errDownload == nil {
			return data, nil
		}
		errLast = errDownload
		var transportError *pluginStoreTransportError
		var limit *RateLimitError
		if ctx.Err() != nil || errors.As(errLast, &limit) || !errors.As(errLast, &transportError) {
			break
		}
	}
	return nil, errLast
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
	data, errDownload := c.get(ctx, releaseURL, "application/vnd.github+json", RequestKindMetadata, 0)
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
	data, finalURL, errDownload := c.getWithURL(ctx, latestURL, "text/html", RequestKindMetadata, 512*1024, func(finalURL string) bool {
		return releaseTagFromURL(finalURL) != ""
	})
	if errDownload != nil {
		return Release{}, errDownload
	}
	tag := releaseTagFromURL(finalURL)
	if tag == "" {
		tag = releaseTagFromHTML(string(data))
	}
	if tag == "" {
		return Release{}, fmt.Errorf("latest release tag not found")
	}
	return releaseForPluginTag(plugin, tag, goos, goarch)
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

// FetchReleaseByTag returns a published release by its exact GitHub tag.
func (c Client) FetchReleaseByTag(ctx context.Context, plugin Plugin, tag string) (Release, error) {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return Release{}, errRepository
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Release{}, fmt.Errorf("release tag is required")
	}
	releaseURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/tags/%s",
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(tag),
	)
	data, errDownload := c.get(ctx, releaseURL, "application/vnd.github+json", RequestKindMetadata, 0)
	if errDownload != nil {
		return Release{}, errDownload
	}
	var release Release
	if errDecode := json.Unmarshal(data, &release); errDecode != nil {
		return Release{}, fmt.Errorf("decode release: %w", errDecode)
	}
	return release, nil
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
	downloadURL := strings.TrimSpace(asset.BrowserDownloadURL)
	apiURL := strings.TrimSpace(asset.APIURL)
	if downloadURL == "" || c.releaseAssetAPIAuthenticated(apiURL) {
		if apiURL != "" {
			downloadURL = apiURL
		}
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("asset %q missing download url", asset.Name)
	}
	return c.get(ctx, downloadURL, "application/octet-stream", RequestKindArtifact, 0)
}

func (c Client) releaseAssetAPIAuthenticated(apiURL string) bool {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return false
	}
	if item, ok := matchingResolvedAuthConfig(c.ResolvedAuth, apiURL, RequestKindArtifact); ok {
		return resolvedAuthConfigured(item)
	}
	return AuthConfigured(c.Auth, apiURL, RequestKindArtifact)
}

func (c Client) get(ctx context.Context, requestURL string, accept string, kind string, maxSize int64) ([]byte, error) {
	data, _, errGet := c.getWithURL(ctx, requestURL, accept, kind, maxSize, nil)
	return data, errGet
}

// getWithURL returns the final URL after authenticated, bounded redirects so
// release discovery can retain the exact published tag, including bare tags.
func (c Client) getWithURL(ctx context.Context, requestURL string, accept string, kind string, maxSize int64, skipBody func(string) bool) (data []byte, finalURL string, errGet error) {
	// A redirect may introduce credentials that were absent at the initial URL.
	// Preserve this binding on failure instead of allowing anonymous recovery.
	authenticatedChain := false
	defer func() {
		if authenticatedChain && errGet != nil {
			errGet = &authenticatedPluginStoreRequestError{err: errGet}
		}
	}()
	currentURL := strings.TrimSpace(requestURL)
	limiter := c.githubRateLimiter()
	for redirects := 0; ; redirects++ {
		if errContext := ctx.Err(); errContext != nil {
			return nil, "", errContext
		}
		headers, authenticated, errAuth := c.authHeaders(currentURL, kind)
		if errAuth != nil {
			return nil, "", errAuth
		}
		authenticatedChain = authenticatedChain || authenticated
		rateKey := githubRateLimitKey(currentURL, c.NetworkScope, headers, authenticated)
		if errLimit := limiter.check(rateKey); errLimit != nil {
			return nil, "", errLimit
		}
		if headers.Get("Accept") == "" {
			headers.Set("Accept", accept)
		}
		if headers.Get("User-Agent") == "" {
			headers.Set("User-Agent", c.userAgent())
		}
		resp, errDo := pluginStoreGetNoRedirect(ctx, c.httpClient(), currentURL, headers)
		if authenticated {
			for name := range headers {
				headers.Del(name)
			}
			if resp != nil && resp.Request != nil {
				resp.Request.Header = nil
			}
		}
		if errDo != nil {
			return nil, "", errDo
		}
		if pluginStoreRedirectStatus(resp.StatusCode) {
			_ = limiter.observe(rateKey, resp.StatusCode, resp.Header, nil)
			nextURL, errRedirect := pluginStoreRedirectURL(resp, currentURL)
			if errClose := resp.Body.Close(); errClose != nil {
				log.WithError(errClose).Debug("failed to close plugin store redirect body")
			}
			if errRedirect != nil {
				return nil, "", errRedirect
			}
			if redirects >= maxPluginStoreRedirects {
				return nil, "", fmt.Errorf("stopped after %d redirects", maxPluginStoreRedirects)
			}
			currentURL = nextURL
			continue
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && skipBody != nil && skipBody(currentURL) {
			_ = limiter.observe(rateKey, resp.StatusCode, resp.Header, nil)
			if errClose := resp.Body.Close(); errClose != nil {
				log.WithError(errClose).Debug("failed to close plugin store release page body")
			}
			return nil, currentURL, nil
		}
		responseData, errRead := readPluginStoreResponse(resp, maxSize, authenticated, limiter, rateKey)
		return responseData, currentURL, errRead
	}
}

// authenticatedPluginStoreRequestError binds failures to the actual request
// chain while keeping typed cooldowns and cancellation available through Unwrap.
type authenticatedPluginStoreRequestError struct {
	err error
}

func (err *authenticatedPluginStoreRequestError) Error() string { return err.err.Error() }
func (err *authenticatedPluginStoreRequestError) Unwrap() error { return err.err }

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

func pluginStoreGetNoRedirect(ctx context.Context, client HTTPDoer, requestURL string, headers http.Header) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create request: %w", errRequest)
	}
	req.Header = headers.Clone()
	resp, errDo := pluginStoreNoRedirectClient(client).Do(req)
	if errDo != nil {
		return nil, pluginStoreRequestError(requestURL, errDo)
	}
	return resp, nil
}

func pluginStoreNoRedirectClient(client HTTPDoer) HTTPDoer {
	httpClient, ok := client.(*http.Client)
	if !ok {
		return client
	}
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func pluginStoreRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func pluginStoreRedirectURL(resp *http.Response, requestURL string) (string, error) {
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return "", fmt.Errorf("redirect missing Location header")
	}
	base, errBase := url.Parse(requestURL)
	if errBase != nil {
		return "", fmt.Errorf("parse redirect base: %w", errBase)
	}
	next, errNext := base.Parse(location)
	if errNext != nil {
		return "", fmt.Errorf("parse redirect location: %w", errNext)
	}
	if next.Scheme == "" || next.Host == "" {
		return "", fmt.Errorf("redirect location is not absolute")
	}
	return next.String(), nil
}

func readPluginStoreResponse(resp *http.Response, maxSize int64, authenticated bool, limiter *GitHubRateLimiter, rateKey string) ([]byte, error) {
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close plugin store response body")
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Apply header-based limits before reading a potentially slow body.
		if errLimit := limiter.observe(rateKey, resp.StatusCode, resp.Header, nil); errLimit != nil {
			return nil, errLimit
		}
		if authenticated && (rateKey == "" || resp.StatusCode != http.StatusForbidden) {
			return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if errLimit := limiter.observe(rateKey, resp.StatusCode, resp.Header, body); errLimit != nil {
			return nil, errLimit
		}
		if authenticated {
			return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_ = limiter.observe(rateKey, resp.StatusCode, resp.Header, nil)
	reader := io.Reader(resp.Body)
	if maxSize > 0 {
		reader = io.LimitReader(resp.Body, maxSize+1)
	}
	data, errRead := io.ReadAll(reader)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if maxSize > 0 && int64(len(data)) > maxSize {
		return nil, fmt.Errorf("response exceeds maximum allowed size of %d bytes", maxSize)
	}
	return data, nil
}

// pluginStoreTransportError marks failures raised before a response was
// received, so callers can retry idempotent GETs without retrying HTTP status
// errors such as rate limits.
type pluginStoreTransportError struct {
	err error
}

func (err *pluginStoreTransportError) Error() string { return err.err.Error() }
func (err *pluginStoreTransportError) Unwrap() error { return err.err }

func pluginStoreRequestError(requestURL string, err error) error {
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	safeURL := "plugin store url"
	if errParse == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		safeURL = parsed.String()
	}
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		err = urlError.Err
	}
	return &pluginStoreTransportError{err: fmt.Errorf("request %s failed: %w", safeURL, err)}
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
