package management

import (
	"archive/zip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (h *Handler) authFileDisplayType(name string) string {
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil {
				continue
			}
			if auth.FileName == name || auth.ID == name {
				provider := strings.TrimSpace(auth.Provider)
				if provider != "" {
					return authProviderDisplayName(provider)
				}
			}
		}
	}
	if h == nil || h.cfg == nil {
		return "其他"
	}
	data, err := os.ReadFile(filepath.Join(h.cfg.AuthDir, name))
	if err != nil {
		return "其他"
	}
	typeValue := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	if typeValue == "" {
		typeValue = strings.TrimSpace(gjson.GetBytes(data, "provider").String())
	}
	return authProviderDisplayName(typeValue)
}

func authProviderDisplayName(provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	switch provider {
	case "antigravity":
		return "Antigravity"
	case "gemini-cli":
		return "GeminiCLI"
	case "gemini":
		return "Gemini"
	case "claude", "anthropic":
		return "Claude"
	case "codex":
		return "Codex"
	case "kimi":
		return "Kimi"
	case "xai":
		return "xAI"
	case "aistudio":
		return "AIStudio"
	case "qwen":
		return "Qwen"
	case "iflow":
		return "iFlow"
	case "vertex":
		return "Vertex"
	default:
		return "其他"
	}
}

func safeZipDownloadName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "其他"
	}
	var b strings.Builder
	for _, r := range value {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			b.WriteRune('-')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "其他"
	}
	return out
}

type downloadAuthFilesZipRequest struct {
	Name  []string `json:"name"`
	Names []string `json:"names"`
}

func authFileZipNamesFromRequest(c *gin.Context) ([]string, error) {
	names := append([]string{}, c.QueryArray("name")...)
	names = append(names, c.QueryArray("names")...)
	if len(names) > 0 || c.Request == nil || c.Request.Method == http.MethodGet || c.Request.Body == nil || c.Request.ContentLength == 0 {
		return names, nil
	}
	var req downloadAuthFilesZipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, err
	}
	names = append(names, req.Name...)
	names = append(names, req.Names...)
	return names, nil
}

// DownloadAuthFilesZip downloads selected auth files as a single zip archive.
func (h *Handler) DownloadAuthFilesZip(c *gin.Context) {
	names, errNames := authFileZipNamesFromRequest(c)
	if errNames != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(names) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	seen := make(map[string]struct{}, len(names))
	cleaned := make([]string, 0, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if isUnsafeAuthFileName(name) || !strings.HasSuffix(strings.ToLower(name), ".json") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid name"})
			return
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	typeCounts := make(map[string]int)
	for _, name := range cleaned {
		typeCounts[h.authFileDisplayType(name)]++
	}
	archiveType := "其他"
	if len(typeCounts) == 1 {
		for value := range typeCounts {
			archiveType = value
		}
	}
	filename := fmt.Sprintf("%s-%d.zip", safeZipDownloadName(archiveType), len(cleaned))
	type zipFileEntry struct {
		name string
		data []byte
	}
	entries := make([]zipFileEntry, 0, len(cleaned))
	for _, name := range cleaned {
		full := filepath.Join(h.cfg.AuthDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			if os.IsNotExist(err) {
				c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read file: %v", err)})
			}
			return
		}
		entries = append(entries, zipFileEntry{name: name, data: data})
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	zw := zip.NewWriter(c.Writer)
	for _, file := range entries {
		entry, errCreate := zw.Create(file.name)
		if errCreate != nil {
			_ = zw.Close()
			return
		}
		if _, errWrite := entry.Write(file.data); errWrite != nil {
			_ = zw.Close()
			return
		}
	}
	if errClose := zw.Close(); errClose != nil {
		log.Errorf("auth files zip close error: %v", errClose)
	}
}
