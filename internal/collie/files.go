package collie

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

// readFile lets the web app show a file an agent mentions (a report it
// wrote, a chart it drew), read-only. Bytes go out as an opaque download so
// a browser never runs them unless ?as=html is requested for an .html/.htm
// file, which renders inside a strict opaque-origin CSP sandbox.
const (
	fileTextMax    = 1 << 20
	fileHTMLMax    = 5 << 20
	fileImageMax   = 20 << 20
	htmlSandboxCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"
)

func isHTMLPath(p string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(p)))
	return ext == ".html" || ext == ".htm"
}

func setHTMLSandboxHeaders(h http.Header) {
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Del("Content-Disposition")
	h.Set("Content-Security-Policy", htmlSandboxCSP)
	h.Set("Cache-Control", "no-store")
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	as := r.URL.Query().Get("as")
	p := s.expand(rawPath)
	if as == "html" && (!isHTMLPath(rawPath) || !isHTMLPath(p)) {
		writeJSON(w, 400, map[string]any{"error": "仅支持直接打开 .html 或 .htm 文件"})
		return
	}
	fi, err := os.Stat(p)
	switch {
	case os.IsNotExist(err):
		if as == "html" && !s.node && s.herdr != nil && s.tryRemoteHTMLFile(w, r, rawPath) {
			return
		}
		writeJSON(w, 404, map[string]any{"error": "找不到这个文件"})
		return
	case err != nil:
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	case fi.IsDir():
		writeJSON(w, 400, map[string]any{"error": "这是一个文件夹"})
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	defer f.Close()

	if as == "html" {
		b, _ := io.ReadAll(io.LimitReader(f, fileHTMLMax))
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			writeJSON(w, 400, map[string]any{"error": "这个文件不是有效的 HTML 文本"})
			return
		}
		h := w.Header()
		setHTMLSandboxHeaders(h)
		h.Set("X-File-Kind", "html")
		h.Set("X-File-Size", strconv.FormatInt(fi.Size(), 10))
		h.Set("X-File-Path", url.PathEscape(s.tilde(p)))
		w.Write(b)
		return
	}

	limit := int64(fileTextMax)
	if as == "image" {
		limit = fileImageMax
	}
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	kind := "text"
	if as == "image" {
		kind = "image"
	} else if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		kind, b = "binary", nil
	}
	if kind == "image" && fi.Size() > limit {
		kind, b = "binary", nil
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	h.Set("X-File-Kind", kind)
	h.Set("X-File-Size", strconv.FormatInt(fi.Size(), 10))
	h.Set("X-File-Path", url.PathEscape(s.tilde(p)))
	w.Write(b)
}

// tryRemoteHTMLFile looks across connected online nodes when a direct ?as=html
// link hits the hub without a machine parameter and the file is not on the hub.
func (s *Server) tryRemoteHTMLFile(w http.ResponseWriter, r *http.Request, rawPath string) bool {
	for _, m := range s.herdr.Machines() {
		if m.Local || !m.Online {
			continue
		}
		client, err := s.herdr.Client(m.ID)
		if err != nil {
			continue
		}
		u := "http://node/api/file?as=html&path=" + url.QueryEscape(rawPath)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			continue
		}
		defer resp.Body.Close()
		h := w.Header()
		for _, k := range []string{"Content-Type", "Content-Security-Policy", "Cache-Control", "X-File-Kind", "X-File-Size", "X-File-Path"} {
			if v := resp.Header.Get(k); v != "" {
				h.Set(k, v)
			}
		}
		if resp.StatusCode == http.StatusOK {
			setHTMLSandboxHeaders(h)
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, io.LimitReader(resp.Body, fileHTMLMax+4096))
		return true
	}
	return false
}
