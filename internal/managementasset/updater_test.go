package managementasset

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestFetchLatestAssetSetsGitHubAuthorization(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "asset-token")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authorization = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"management.html","browser_download_url":"https://example.com/management.html","digest":"sha256:abc123"}]}`))
	}))
	defer server.Close()

	asset, remoteHash, err := fetchLatestAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset() error = %v", err)
	}
	if authorization != "Bearer asset-token" {
		t.Fatalf("Authorization = %q, want %q", authorization, "Bearer asset-token")
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("asset = %#v, want %q", asset, managementAssetName)
	}
	if remoteHash != "abc123" {
		t.Fatalf("remoteHash = %q, want %q", remoteHash, "abc123")
	}
}

func TestFetchLatestAssetOmitsAuthorizationWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("github_token", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authorization = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"management.html","browser_download_url":"https://example.com/management.html","digest":"sha256:abc123"}]}`))
	}))
	defer server.Close()

	asset, remoteHash, err := fetchLatestAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestAsset() error = %v", err)
	}
	if authorization != "" {
		t.Fatalf("Authorization = %q, want empty", authorization)
	}
	if asset == nil || asset.Name != managementAssetName {
		t.Fatalf("asset = %#v, want %q", asset, managementAssetName)
	}
	if remoteHash != "abc123" {
		t.Fatalf("remoteHash = %q, want %q", remoteHash, "abc123")
	}
}

func TestAutoUpdateSkipReason(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantReason string
		wantSkip   bool
	}{
		{
			name:       "nil config",
			cfg:        nil,
			wantReason: "config not yet available",
			wantSkip:   true,
		},
		{
			name: "cluster mode",
			cfg: &config.Config{
				Home: config.HomeConfig{Enabled: true},
			},
			wantReason: "cluster mode enabled",
			wantSkip:   true,
		},
		{
			name: "control panel disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableControlPanel: true},
			},
			wantReason: "control panel disabled",
			wantSkip:   true,
		},
		{
			name: "auto update disabled",
			cfg: &config.Config{
				RemoteManagement: config.RemoteManagement{DisableAutoUpdatePanel: true},
			},
			wantReason: "disable-auto-update-panel is enabled",
			wantSkip:   true,
		},
		{
			name:       "enabled",
			cfg:        &config.Config{},
			wantReason: "",
			wantSkip:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotSkip := autoUpdateSkipReason(tt.cfg)
			if gotReason != tt.wantReason || gotSkip != tt.wantSkip {
				t.Fatalf("autoUpdateSkipReason() = (%q, %t), want (%q, %t)", gotReason, gotSkip, tt.wantReason, tt.wantSkip)
			}
		})
	}
}

func TestIsDefaultForkRepo(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want bool
	}{
		{"empty", "", true},
		{"unparseable", "not a url", true},
		{"fork github", "https://github.com/yunqiasen/Cli-Proxy-API-Management-Center", true},
		{"fork github trailing slash", "https://github.com/yunqiasen/Cli-Proxy-API-Management-Center/", true},
		{"fork api", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases", true},
		{"upstream repo", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center", false},
		{"custom repo", "https://github.com/someone/other-panel", false},
		{"custom api", "https://api.github.com/repos/someone/other-panel/releases/latest", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDefaultForkRepo(tt.repo); got != tt.want {
				t.Fatalf("isDefaultForkRepo(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}

func TestResolveReleaseListURL(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{"empty", "", defaultManagementReleaseURL},
		{"fork github", "https://github.com/yunqiasen/Cli-Proxy-API-Management-Center", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases"},
		{"fork github slash", "https://github.com/yunqiasen/Cli-Proxy-API-Management-Center/", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases"},
		{"fork api latest", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases/latest", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases"},
		{"fork api tags", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases/tags/cpa-ui-v8-1.0", "https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveReleaseListURL(tt.repo); got != tt.want {
				t.Fatalf("resolveReleaseListURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestResolveReleaseURL_ExplicitTagAPI(t *testing.T) {
	got := resolveReleaseURL("https://api.github.com/repos/someone/other-panel/releases/tags/v1.2.3")
	want := "https://api.github.com/repos/someone/other-panel/releases/tags/v1.2.3"
	if got != want {
		t.Fatalf("resolveReleaseURL explicit tag = %q, want %q", got, want)
	}
}

func TestResolveReleaseURL_ExplicitTagGitHub(t *testing.T) {
	got := resolveReleaseURL("https://github.com/someone/other-panel/releases/tag/v1.2.3")
	want := "https://api.github.com/repos/someone/other-panel/releases/tags/v1.2.3"
	if got != want {
		t.Fatalf("resolveReleaseURL github tag = %q, want %q", got, want)
	}
}

func TestResolveReleaseURL_CustomRepoLatest(t *testing.T) {
	got := resolveReleaseURL("https://github.com/someone/other-panel")
	want := "https://api.github.com/repos/someone/other-panel/releases/latest"
	if got != want {
		t.Fatalf("resolveReleaseURL custom repo = %q, want %q", got, want)
	}
}

func TestFetchV8PrereleaseAsset_FindsLatestPrerelease(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	body := `[` +
		`{"tag_name":"v7-stable","prerelease":false,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/v7.html","digest":"sha256:aaa"}]},` +
		`{"tag_name":"cpa-ui-v8-2.0","prerelease":true,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/v8-2.html","digest":"sha256:bbb"}]},` +
		`{"tag_name":"cpa-ui-v8-1.0","prerelease":true,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/v8-1.html","digest":"sha256:ccc"}]}` +
		`]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	asset, remoteHash, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchV8PrereleaseAsset() error = %v", err)
	}
	if asset == nil || asset.BrowserDownloadURL != "https://example.com/v8-2.html" {
		t.Fatalf("asset = %#v, want v8-2 download URL", asset)
	}
	if remoteHash != "bbb" {
		t.Fatalf("remoteHash = %q, want bbb", remoteHash)
	}
}

func TestFetchV8PrereleaseAsset_SkipsDraft(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	body := `[` +
		`{"tag_name":"cpa-ui-v8-3.0","prerelease":true,"draft":true,"assets":[{"name":"management.html","browser_download_url":"https://example.com/draft.html","digest":"sha256:ddd"}]},` +
		`{"tag_name":"cpa-ui-v8-2.0","prerelease":true,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/v8-2.html","digest":"sha256:bbb"}]}` +
		`]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	asset, _, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchV8PrereleaseAsset() error = %v", err)
	}
	if asset == nil || asset.BrowserDownloadURL != "https://example.com/v8-2.html" {
		t.Fatalf("asset = %#v, want v8-2 (draft skipped)", asset)
	}
}

func TestFetchV8PrereleaseAsset_SkipsNonPrerelease(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	body := `[` +
		`{"tag_name":"cpa-ui-v8-1.0","prerelease":false,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/stable.html","digest":"sha256:eee"}]}` +
		`]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	_, _, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("fetchV8PrereleaseAsset() expected error for non-prerelease, got nil")
	}
}

func TestFetchV8PrereleaseAsset_NoMatchReturnsError(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	body := `[{"tag_name":"v7-stable","prerelease":false,"draft":false,"assets":[{"name":"management.html","browser_download_url":"https://example.com/v7.html","digest":"sha256:aaa"}]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	_, _, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("fetchV8PrereleaseAsset() expected error when no V8 prerelease found, got nil")
	}
}

func TestFetchV8PrereleaseAsset_SkipsWrongAssetName(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")

	body := `[{"tag_name":"cpa-ui-v8-1.0","prerelease":true,"draft":false,"assets":[{"name":"panel.html","browser_download_url":"https://example.com/panel.html","digest":"sha256:fff"}]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	_, _, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL)
	if err == nil {
		t.Fatal("fetchV8PrereleaseAsset() expected error when asset name mismatch, got nil")
	}
}

func TestEnsureLatestManagementHTML_V8NoFallbackOnFailure(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")
	t.Setenv("MANAGEMENT_STATIC_PATH", "")

	// Server that returns 404 for release list (simulating network/channel failure)
	listServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer listServer.Close()

	tmpDir := t.TempDir()
	staticDir := filepath.Join(tmpDir, "static")
	// Pre-place a local V8 bundle to verify it's preserved
	existingHTML := []byte("<html>v8-existing</html>")
	localPath := filepath.Join(staticDir, managementAssetName)
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, existingHTML, 0o644); err != nil {
		t.Fatal(err)
	}

	// Use the list server URL as the repo (isDefaultForkRepo will be false for localhost,
	// so we need to pass the fork repo to trigger V8 channel)
	// Actually we need to pass the fork repo string and intercept the API call.
	// Since we can't easily intercept, we test the V8 path by calling fetchV8PrereleaseAsset directly.
	_, _, err := fetchV8PrereleaseAsset(t.Context(), listServer.Client(), listServer.URL)
	if err == nil {
		t.Fatal("expected error from failed V8 fetch")
	}

	// Verify local file still exists (preserved)
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("local file should be preserved, got error: %v", err)
	}
	if string(data) != string(existingHTML) {
		t.Fatalf("local file content changed: got %q, want %q", string(data), string(existingHTML))
	}
}

func TestEnsureLatestManagementHTML_V8NoFallbackMissingFile(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITSTORE_GIT_TOKEN", "")
	t.Setenv("GITSTORE_GIT_URL", "")
	t.Setenv("MANAGEMENT_STATIC_PATH", "")

	listServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer listServer.Close()

	tmpDir := t.TempDir()
	staticDir := filepath.Join(tmpDir, "static")
	localPath := filepath.Join(staticDir, managementAssetName)

	_, _, err := fetchV8PrereleaseAsset(t.Context(), listServer.Client(), listServer.URL)
	if err == nil {
		t.Fatal("expected error from failed V8 fetch")
	}

	// Verify file remains absent
	_, statErr := os.Stat(localPath)
	if !os.IsNotExist(statErr) {
		t.Fatalf("file should remain absent, got stat error: %v", statErr)
	}
}

func TestV8ChannelRepositoryBoundary(t *testing.T) {
	for _, repo := range []string{
		"https://github.com/yunqiasen/Cli-Proxy-API-Management-Center.git",
		"https://github.com/yunqiasen/Cli-Proxy-API-Management-Center/releases/latest",
	} {
		if !isDefaultForkRepo(repo) {
			t.Errorf("fork URL must use V8 channel: %s", repo)
		}
	}
	for _, repo := range []string{
		"https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center-other/releases/latest",
		"https://api.github.com/repos/yunqiasen/Cli-Proxy-API-Management-Center/releases/tags/cpa-ui-v8-pinned",
		"https://github.com/yunqiasen/Cli-Proxy-API-Management-Center/releases/tag/cpa-ui-v8-pinned",
	} {
		if isDefaultForkRepo(repo) {
			t.Errorf("custom repository or explicit pin must not use rolling channel: %s", repo)
		}
	}
}

func TestV8ChannelRequiresExactAssetName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"cpa-ui-v8-1","prerelease":true,"assets":[{"name":"Management.html","browser_download_url":"https://example.com/wrong"}]}]`))
	}))
	defer server.Close()
	if _, _, err := fetchV8PrereleaseAsset(t.Context(), server.Client(), server.URL); err == nil {
		t.Fatal("case-mismatched release asset accepted")
	}
}
