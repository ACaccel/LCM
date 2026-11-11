package ingress

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nginxBinary is the nginx the host would run the site with.
const nginxBinary = "/usr/sbin/nginx"

// TestRenderedSiteLoadsInNginx runs "nginx -t" of the host, when there is
// one, on the rendered sites, so that a directive nginx does not accept
// shows up here. The listen addresses become loopback ports above 1024 and
// the certificate a throwaway one, so that it runs without root; nothing
// is started.
func TestRenderedSiteLoadsInNginx(t *testing.T) {
	if _, err := os.Stat(nginxBinary); err != nil {
		t.Skip("no nginx; the container end-to-end test loads the site")
	}
	dir := t.TempDir()
	cert, key := selfSigned(t, dir)
	for name, s := range map[string]Site{"live-shape": liveShape(), "minimal": {Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme",
		Routes: []Route{{Service: "docs", Prefix: "/", Static: "/srv/docs", Options: Options{Static: &Static{SPA: true, Immutable: []string{"assets/"}}}}}}} {
		t.Run(name, func(t *testing.T) {
			s.Listen = []string{"127.0.0.1"}
			if s.TLS != nil {
				s.TLS = &TLSFiles{Certificate: cert, Key: key}
			}
			out, err := Render(s)
			if err != nil {
				t.Fatal(err)
			}
			site := strings.NewReplacer("listen 127.0.0.1:80;", "listen 127.0.0.1:18080;", "listen 127.0.0.1:443 ssl;", "listen 127.0.0.1:18443 ssl;").Replace(string(out))
			prefix := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Join(prefix, "conf"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prefix, "conf", "site.conf"), []byte(site), 0o600); err != nil {
				t.Fatal(err)
			}
			main := "events {}\nhttp {\n    access_log off;\n    include site.conf;\n}\n"
			if err := os.WriteFile(filepath.Join(prefix, "conf", "nginx.conf"), []byte(main), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, nginxBinary, "-t", "-q", "-p", prefix, "-c", "conf/nginx.conf", "-e", "stderr", //nolint:gosec // a fixed program on files the test wrote
				"-g", "pid "+filepath.Join(prefix, "nginx.pid")+";")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nginx -t: %v\n%s\n%s", err, out, site)
			}
		})
	}
}

// selfSigned writes a throwaway certificate and key for example.org.
func selfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.org"}, DNSNames: []string{"example.org"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, key
}
