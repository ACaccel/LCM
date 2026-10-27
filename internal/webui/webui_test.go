package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<html>app</html>")},
		"assets/app-abc.js": {Data: []byte("console.log(1)")},
		"assets/app-abc.js.gz": {
			Data: []byte("gzipped"),
		},
		"favicon.svg": {Data: []byte("<svg/>")},
	}
}

func get(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesIndexAndFallsBackForRoutes(t *testing.T) {
	h := newHandler(builtFS())
	for _, target := range []string{"/", "/services", "/services/my-svc/timeline", "/policies/p"} {
		rec := get(h, "GET", target)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>app</html>" {
			t.Fatalf("GET %s = %d %q", target, rec.Code, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q", target, cc)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(newHandler(builtFS()), "GET", "/")
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "script-src 'self' 'unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") == "" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestHashedAssetsAreCachedForever(t *testing.T) {
	rec := get(newHandler(builtFS()), "GET", "/assets/app-abc.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestGzipTwinIsUsedWhenAccepted(t *testing.T) {
	h := newHandler(builtFS())
	rec := get(h, "GET", "/assets/app-abc.js", "Accept-Encoding", "br, gzip;q=0.8")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.String() != "gzipped" {
		t.Fatalf("gzip = %v %q", rec.Header(), rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("gzip Content-Type = %q", ct)
	}
	for _, enc := range []string{"", "identity", "gzip;q=0"} {
		rec := get(h, "GET", "/assets/app-abc.js", "Accept-Encoding", enc)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "console.log(1)" {
			t.Errorf("Accept-Encoding %q served %v %q", enc, rec.Header(), rec.Body.String())
		}
	}
}

func TestMissingAssetAndAPIPathsAre404(t *testing.T) {
	h := newHandler(builtFS())
	for _, target := range []string{"/assets/missing.js", "/robots.txt", "/api/v1/nothing", "/api"} {
		if rec := get(h, "GET", target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

func TestMethods(t *testing.T) {
	h := newHandler(builtFS())
	if rec := get(h, "POST", "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
	rec := get(h, "HEAD", "/")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

func TestNotBuiltPage(t *testing.T) {
	h := newHandler(fstest.MapFS{".gitkeep": {}})
	rec := get(h, "GET", "/services")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make build") {
		t.Fatalf("not built = %d %q", rec.Code, rec.Body.String())
	}
}

func TestEmbeddedPlaceholderBuilds(t *testing.T) {
	// Whatever dist/ holds, the embedded handler must answer without panic.
	if rec := get(Handler(), "GET", "/"); rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("embedded GET / = %d", rec.Code)
	}
}

// noSeekFS hides Seek so the fallback path that reads the whole file runs.
type noSeekFS struct{ fs.FS }

func (n noSeekFS) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return struct{ fs.File }{f}, nil
}

func TestServesFilesThatCannotSeek(t *testing.T) {
	rec := get(newHandler(noSeekFS{builtFS()}), "GET", "/assets/app-abc.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body.String())
	}
}
