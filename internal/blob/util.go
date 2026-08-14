package blob

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

func cleanKey(key string) (string, error) {
	raw := strings.ReplaceAll(strings.TrimSpace(key), "\\", "/")
	if raw == "" || strings.Contains(raw, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	clean := path.Clean("/" + raw)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	return clean, nil
}

func joinPrefix(prefix, key string) string {
	prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
	if prefix == "" {
		return key
	}
	return prefix + "/" + key
}

func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}
