package pluginstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the installer, including archive verification and the shared cooldown.
func TestInstallPublicReleaseDuringGitHubAPICooldown(t *testing.T) {
	for _, mode := range []string{"latest", "pinned-v", "pinned-bare", "manifest"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			plugin := testPlugin()
			plugin.Version = "" // Store entries need not advertise a version.
			tag := "v0.2.0"
			if mode == "pinned-bare" {
				tag = "0.2.0"
			}
			archive := makeZip(t, map[string]string{"sample-provider.so": "verified-plugin"})
			archiveName := ArchiveName(plugin.ID, "0.2.0", "linux", "amd64")
			sum := sha256.Sum256(archive)
			apiCalls, webCalls := 0, 0
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			limiter := &GitHubRateLimiter{nowFunc: func() time.Time { return now }}
			client := Client{RateLimiter: limiter, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "" {
					t.Fatal("credentials reached public release")
				}
				switch {
				case req.URL.Host == "api.github.com":
					apiCalls++
					return testHTTPResponse(req, 403, githubTestHeaders(map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(now.Add(time.Hour).Unix())}), `{"message":"API rate limit exceeded"}`), nil
				case strings.HasSuffix(req.URL.Path, "/releases/latest"):
					webCalls++
					return testHTTPResponse(req, 302, githubTestHeaders(map[string]string{"Location": "https://github.com/author-name/cliproxy-sample-provider-plugin/releases/tag/" + tag}), ""), nil
				case strings.HasSuffix(req.URL.Path, "/releases/tag/"+tag):
					webCalls++
					return testHTTPResponse(req, 200, nil, `<html>release</html>`), nil
				case strings.Contains(req.URL.Path, "/releases/download/"+tag+"/"):
					if strings.HasSuffix(req.URL.Path, "/checksums.txt") {
						return testHTTPResponse(req, 200, nil, hex.EncodeToString(sum[:])+"  "+archiveName+"\n"), nil
					}
					if strings.HasSuffix(req.URL.Path, "/"+archiveName) {
						return testHTTPBytesResponse(req, 200, nil, archive), nil
					}
				}
				return testHTTPResponse(req, 404, nil, "not found"), nil
			})}
			// A catalog lookup exhausts the IP budget before the installation starts.
			_, errLookup := client.FetchLatestRelease(context.Background(), plugin)
			var limit *RateLimitError
			if !errors.As(errLookup, &limit) {
				t.Fatalf("lookup error=%v", errLookup)
			}
			for range 2 {
				options := InstallOptions{PluginsDir: root, GOOS: "linux", GOARCH: "amd64"}
				var result InstallResult
				var errInstall error
				switch mode {
				case "latest":
					result, errInstall = client.Install(context.Background(), plugin, options)
				case "manifest":
					source := Source{ID: "official", Name: "Official", URL: DefaultRegistryURL}
					manifest, errManifest := ManifestFromRelease(source, plugin, Release{TagName: tag})
					if errManifest != nil {
						t.Fatal(errManifest)
					}
					result, errInstall = client.InstallManifest(context.Background(), manifest, options)
				default:
					result, errInstall = client.InstallVersion(context.Background(), plugin, tag, "0.2.0", options)
				}
				if errInstall != nil {
					t.Fatalf("public release installation failed during API cooldown: %v", errInstall)
				}
				if result.Version != "0.2.0" || result.ReleaseTag != tag {
					t.Fatalf("wrong release: %#v", result)
				}
				data, errRead := os.ReadFile(filepath.Join(root, "linux", "amd64", "sample-provider-v0.2.0.so"))
				if errRead != nil || string(data) != "verified-plugin" {
					t.Fatalf("installed data=%q error=%v", data, errRead)
				}
			}
			if apiCalls != 1 || (mode == "latest" && webCalls == 0) {
				t.Fatalf("API calls=%d web calls=%d; want one API rejection and public recovery", apiCalls, webCalls)
			}
		})
	}
}

func TestInstallAuthenticatedReleasePreservesGitHubAPICooldown(t *testing.T) {
	t.Setenv("PLUGIN_RECOVERY_TOKEN", "private-token")
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, Auth: []AuthConfig{{Match: "https://api.github.com/", Type: AuthTypeGitHubToken, TokenEnv: "PLUGIN_RECOVERY_TOKEN"}}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" {
			t.Fatal("authenticated release fell back to public transport")
		}
		return testHTTPResponse(req, 429, githubTestHeaders(map[string]string{"Retry-After": "3600"}), "limited"), nil
	})}
	_, errInstall := client.Install(context.Background(), testPlugin(), InstallOptions{PluginsDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"})
	var limit *RateLimitError
	if !errors.As(errInstall, &limit) || calls != 1 {
		t.Fatalf("error=%v calls=%d", errInstall, calls)
	}
}

func TestInstallPublicReleaseRecoveryRejectsBadChecksum(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archiveName := ArchiveName("sample-provider", "0.2.0", "linux", "amd64")
	client := Client{RateLimiter: &GitHubRateLimiter{}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.github.com" {
			return testHTTPResponse(req, 429, githubTestHeaders(map[string]string{"Retry-After": "3600"}), "limited"), nil
		}
		if strings.HasSuffix(req.URL.Path, "/checksums.txt") {
			return testHTTPResponse(req, 200, nil, strings.Repeat("0", 64)+"  "+archiveName+"\n"), nil
		}
		return testHTTPBytesResponse(req, 200, nil, makeZip(t, map[string]string{"sample-provider.so": "corrupted"})), nil
	})}
	_, errInstall := client.InstallVersion(context.Background(), testPlugin(), "v0.2.0", "0.2.0", InstallOptions{PluginsDir: root, GOOS: "linux", GOARCH: "amd64"})
	if errInstall == nil || !strings.Contains(errInstall.Error(), "checksum mismatch") {
		t.Fatalf("error=%v", errInstall)
	}
	entries, errRead := os.ReadDir(root)
	if errRead != nil || len(entries) != 0 {
		t.Fatalf("checksum failure wrote plugin files: %v %v", entries, errRead)
	}
}

func TestInstallTagScopedCredentialsPreventPublicRecovery(t *testing.T) {
	t.Setenv("PLUGIN_RECOVERY_TOKEN", "private-token")
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, Auth: []AuthConfig{{Match: "https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/tags/", Type: AuthTypeGitHubToken, TokenEnv: "PLUGIN_RECOVERY_TOKEN"}}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" {
			t.Fatal("tag-scoped credential request downgraded to public download")
		}
		return testHTTPResponse(req, 429, githubTestHeaders(map[string]string{"Retry-After": "3600"}), "limited"), nil
	})}
	_, errInstall := client.InstallVersion(context.Background(), testPlugin(), "v0.2.0", "0.2.0", InstallOptions{PluginsDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"})
	var limit *RateLimitError
	if !errors.As(errInstall, &limit) || calls != 1 {
		t.Fatalf("error=%v calls=%d", errInstall, calls)
	}
}

func TestInstallCanceledRequestDoesNotStartPublicRecovery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	_, errInstall := client.Install(ctx, testPlugin(), InstallOptions{PluginsDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"})
	if !errors.Is(errInstall, context.Canceled) || calls != 1 {
		t.Fatalf("error=%v calls=%d", errInstall, calls)
	}
}

func TestLatestWebRecoveryPreservesBareReleaseTag(t *testing.T) {
	t.Parallel()
	plugin := testPlugin()
	client := Client{HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/latest") {
			return testHTTPResponse(req, 302, githubTestHeaders(map[string]string{"Location": "https://github.com/author-name/cliproxy-sample-provider-plugin/releases/tag/0.2.0"}), ""), nil
		}
		return testHTTPResponse(req, 200, nil, "release"), nil
	})}
	release, errRelease := client.fetchLatestReleaseFromWeb(context.Background(), plugin, "linux", "amd64")
	if errRelease != nil || release.TagName != "0.2.0" {
		t.Fatalf("release=%#v error=%v", release, errRelease)
	}
	if !strings.Contains(release.Assets[0].BrowserDownloadURL, "/download/0.2.0/") {
		t.Fatalf("wrong fixed tag URL: %s", release.Assets[0].BrowserDownloadURL)
	}
}

func TestInstallRedirectedCredentialsPreventPublicRecovery(t *testing.T) {
	t.Setenv("PLUGIN_RECOVERY_TOKEN", "private-token")
	calls := 0
	client := Client{RateLimiter: &GitHubRateLimiter{}, Auth: []AuthConfig{{Match: "https://api.github.com/repos/private/repo/", Type: AuthTypeGitHubToken, TokenEnv: "PLUGIN_RECOVERY_TOKEN"}}, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api.github.com" {
			t.Fatal("credential-bound redirect downgraded to public recovery")
		}
		if calls == 1 {
			return testHTTPResponse(req, 302, githubTestHeaders(map[string]string{"Location": "https://api.github.com/repos/private/repo/releases/latest"}), ""), nil
		}
		if req.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("redirect auth rule not applied")
		}
		return testHTTPResponse(req, 429, githubTestHeaders(map[string]string{"Retry-After": "3600"}), "limited"), nil
	})}
	_, errInstall := client.Install(context.Background(), testPlugin(), InstallOptions{PluginsDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"})
	var limit *RateLimitError
	if !errors.As(errInstall, &limit) || calls != 2 {
		t.Fatalf("error=%v calls=%d", errInstall, calls)
	}
}

func TestLatestWebRecoveryDoesNotReadPageWhenRedirectSuppliesTag(t *testing.T) {
	t.Parallel()
	body := &trackingReadCloser{data: []byte(strings.Repeat("x", 512*1024+1))}
	client := Client{HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/latest") {
			return testHTTPResponse(req, 302, githubTestHeaders(map[string]string{"Location": "https://github.com/author-name/cliproxy-sample-provider-plugin/releases/tag/v0.2.0"}), ""), nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: req}, nil
	})}
	release, errRelease := client.fetchLatestReleaseFromWeb(context.Background(), testPlugin(), "linux", "amd64")
	if errRelease != nil || release.TagName != "v0.2.0" || body.offset != 0 {
		t.Fatalf("release=%#v error=%v page bytes read=%d", release, errRelease, body.offset)
	}
}
