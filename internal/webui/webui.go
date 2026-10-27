// Package webui serves the web console, a single-page application that is
// built from web/ into dist/ and embedded in the binary.
//
// A committed empty placeholder keeps the embed directive valid when the UI
// has not been built (for example in a Go-only checkout). In that case every
// request answers with a short page that says how to build it.
package webui

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

const (
	indexName = "index.html"
	// assetDir holds the content-hashed files that Vite emits; they never
	// change under the same name and may be cached for a year.
	assetDir = "assets/"

	// The console needs inline styles only: CodeMirror injects <style>
	// elements and React Flow positions nodes with style attributes. Scripts
	// stay restricted to same-origin files.
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
)

// Handler serves the embedded console. Mount it below the API: it answers 404
// for anything under /api/ so that a mistyped API path is never answered with
// the application shell.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// The embed directive guarantees the directory exists.
		panic("webui: " + err.Error())
	}
	return newHandler(sub)
}

func newHandler(fsys fs.FS) http.Handler {
	return &handler{fs: fsys}
}

type handler struct {
	fs fs.FS
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	if _, err := fs.Stat(h.fs, indexName); err != nil {
		serveNotBuilt(w, r)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexName
	}
	if st, err := fs.Stat(h.fs, name); err != nil || st.IsDir() {
		// A missing file with an extension is a broken asset reference, not a
		// client-side route; answering it with HTML would hide the problem.
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = indexName
	}
	h.serveFile(w, r, name)
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	hdr := w.Header()
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		hdr.Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, assetDir) {
		hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hdr.Set("Cache-Control", "no-cache")
	}

	// The build step writes a gzip twin next to compressible files, so the
	// server never spends CPU or memory compressing at request time.
	src := name
	if acceptsGzip(r) {
		if _, err := fs.Stat(h.fs, name+".gz"); err == nil {
			src = name + ".gz"
			hdr.Set("Content-Encoding", "gzip")
		}
	}
	hdr.Add("Vary", "Accept-Encoding")

	f, err := h.fs.Open(src)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	// Embedded files are seekable, so they are streamed straight from the
	// binary; copying a whole bundle into memory per request would only
	// add garbage. Other file systems fall back to reading the file.
	body, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		body = bytes.NewReader(data)
	}
	// ServeContent adds Range and HEAD handling; the zero modification time
	// suppresses Last-Modified, which an embedded file cannot provide.
	http.ServeContent(w, r, name, time.Time{}, body)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(part, ";")
		if strings.TrimSpace(enc) != "gzip" {
			continue
		}
		q := strings.TrimSpace(params)
		if v, ok := strings.CutPrefix(q, "q="); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && f > 0
		}
		return true
	}
	return false
}

func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
}

const notBuiltPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>LCM: web console not built</title></head>
<body>
<h1>The web console is not built</h1>
<p>This binary was compiled without the web UI. The API is available under <code>/api/v1</code>.</p>
<p>To include the console, build it and rebuild the binary:</p>
<pre>make build</pre>
<p>This needs Node.js 24 and npm; see the README for details.</p>
</body>
</html>
`

func serveNotBuilt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(notBuiltPage))
	}
}
