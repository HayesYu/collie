package collie

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Images in a transcript are base64 inside the log. Each one is written once
// to ~/.collie/images/, named by its content, and the message carries
// "[image:~/.collie/images/<hash>.png]" for the web app to show by path.

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func imageToken(src imageSource) string {
	ext, ok := imageExt[src.MediaType]
	if src.Type != "base64" || !ok || src.Data == "" {
		return "[image]"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "[image]"
	}
	sum := sha1.Sum([]byte(src.Data))
	name := hex.EncodeToString(sum[:10]) + ext
	p := filepath.Join(home, ".collie", "images", name)
	if _, err := os.Stat(p); err != nil {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(src.Data))
		if err != nil {
			return "[image]"
		}
		os.MkdirAll(filepath.Dir(p), 0o700)
		tmp := p + ".tmp"
		if os.WriteFile(tmp, b, 0o600) != nil || os.Rename(tmp, p) != nil {
			os.Remove(tmp)
			return "[image]"
		}
	}
	return "[image:~/.collie/images/" + name + "]"
}
