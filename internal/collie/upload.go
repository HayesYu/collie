package collie

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Files added in a composer are saved on the machine where the work happens
// (the hub, or the node the request is forwarded to), and their paths are
// put into the message: agents read files by path.

const maxUpload = 25 << 20

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

func uploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := filepath.Ext(name)
	stem := strings.Trim(unsafeName.ReplaceAllString(strings.TrimSuffix(name, ext), "-"), "-.")
	ext = unsafeName.ReplaceAllString(ext, "")
	if r := []rune(stem); len(r) > 60 {
		stem = string(r[:60])
	}
	if stem == "" {
		stem = "file"
	}
	return stem + ext
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	name := uploadName(r.URL.Query().Get("name"))
	dir := filepath.Join(collieDir(), "uploads", time.Now().Format("20060102"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(w, err)
		return
	}
	path := filepath.Join(dir, randomToken()[:6]+"-"+name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fail(w, err)
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	f.Close()
	if err != nil {
		os.Remove(path)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, 413, map[string]any{"error": "文件太大了（最多 25 MB）"})
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"path": path, "name": name, "size": n})
}
