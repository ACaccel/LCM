package monitor

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
)

var tlsNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example Test CA"},
		NotBefore: tlsNow.Add(-365 * day), NotAfter: tlsNow.Add(3650 * day),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// serve starts a TLS server whose leaf for names is signed by ca and valid
// until notAfter, and returns its port.
func (ca *testCA) serve(t *testing.T, notAfter time.Time, names ...string) int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: tlsNow.Add(-30 * day), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.(*tls.Conn).Handshake()
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func tlsEnv(clk clock.Clock, roots *x509.CertPool) Env {
	return Env{Clock: clk, Roots: roots}
}

func TestTLSProbe(t *testing.T) {
	ca := newTestCA(t)
	other := newTestCA(t)
	tests := []struct {
		name   string
		port   int
		props  map[string]any
		roots  *x509.CertPool
		ok     bool
		wantIn string
	}{
		{"valid", ca.serve(t, tlsNow.Add(90*day+time.Hour), "example.org"), nil, ca.pool, true, "valid for 90 days (notAfter 2026-08-30)"},
		{"expiring soon", ca.serve(t, tlsNow.Add(10*day+time.Hour), "example.org"), nil, ca.pool, false, "expires in 10 days (notAfter 2026-06-11), below the required 21 days"},
		{"custom minValidity", ca.serve(t, tlsNow.Add(10*day+time.Hour), "example.org"), map[string]any{"minValidity": "168h"}, ca.pool, true, "valid for 10 days"},
		{"expired", ca.serve(t, tlsNow.Add(-day), "example.org"), nil, ca.pool, false, "certificate verification failed: x509: certificate has expired"},
		{"wrong name", ca.serve(t, tlsNow.Add(90*day), "www.example.net"), nil, ca.pool, false, "certificate is valid for www.example.net, not example.org"},
		{"serverName override", ca.serve(t, tlsNow.Add(90*day), "www.example.net"), map[string]any{"serverName": "www.example.net"}, ca.pool, true, "valid for 90 days"},
		{"untrusted root", ca.serve(t, tlsNow.Add(90*day), "example.org"), nil, other.pool, false, "certificate signed by unknown authority"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := map[string]any{"host": "127.0.0.1", "port": tt.port, "serverName": "example.org"}
			for k, v := range tt.props {
				props[k] = v
			}
			r := probe(t, "tls", props, tlsEnv(clock.NewFake(tlsNow), tt.roots))
			if r.OK != tt.ok || !strings.Contains(r.Message, tt.wantIn) {
				t.Fatalf("result = %+v, want ok=%v containing %q", r, tt.ok, tt.wantIn)
			}
		})
	}
}

func TestTLSProbeServerNameDefaultsToHost(t *testing.T) {
	ca := newTestCA(t)
	port := ca.serve(t, tlsNow.Add(90*day), "example.org")
	// The certificate carries no IP address, so the default name 127.0.0.1 fails.
	r := probe(t, "tls", map[string]any{"host": "127.0.0.1", "port": port}, tlsEnv(clock.NewFake(tlsNow), ca.pool))
	if r.OK || !strings.Contains(r.Message, "127.0.0.1") {
		t.Fatalf("result = %+v", r)
	}
}

func TestTLSProbeConnectFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	r := probe(t, "tls", map[string]any{"host": "127.0.0.1", "port": port}, tlsEnv(clock.NewFake(tlsNow), nil))
	if r.OK || r.Message == "" {
		t.Fatalf("result = %+v", r)
	}
}

func TestTLSProbeUsesInjectedClock(t *testing.T) {
	ca := newTestCA(t)
	port := ca.serve(t, tlsNow.Add(60*day), "example.org")
	clk := clock.NewFake(tlsNow)
	b := buildCheck(t, "tls", map[string]any{"host": "127.0.0.1", "port": port, "serverName": "example.org"}, tlsEnv(clk, ca.pool))
	if r := b.Probe(t.Context()); !r.OK {
		t.Fatalf("before advancing: %+v", r)
	}
	clk.Advance(45 * day)
	r := b.Probe(t.Context())
	if r.OK || !strings.Contains(r.Message, "expires in 15 days") {
		t.Fatalf("after advancing: %+v", r)
	}
}

func TestTLSBuildValidation(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]any
		path  string
	}{
		{"no host", map[string]any{}, "props.host"},
		{"host with scheme", map[string]any{"host": "https://example.org"}, "props.host"},
		{"host with port", map[string]any{"host": "example.org:443"}, "props.host"},
		{"host with path", map[string]any{"host": "example.org/x"}, "props.host"},
		{"port too large", map[string]any{"host": "example.org", "port": 70000}, "props.port"},
		{"negative port", map[string]any{"host": "example.org", "port": -1}, "props.port"},
		{"bad serverName", map[string]any{"host": "example.org", "serverName": "a b"}, "props.serverName"},
		{"minValidity too short", map[string]any{"host": "example.org", "minValidity": "30m"}, "props.minValidity"},
		{"negative minValidity", map[string]any{"host": "example.org", "minValidity": "-24h"}, "props.minValidity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DefaultRegistry().Build(model.Check{Name: "c", Type: "tls", Props: tt.props}.WithDefaults(), Env{})
			var is model.Issues
			if !errors.As(err, &is) || len(is) != 1 || is[0].Path != tt.path {
				t.Fatalf("err = %v, want one issue at %s", err, tt.path)
			}
		})
	}
	if _, err := DefaultRegistry().Build(model.Check{Name: "c", Type: "tls", Props: map[string]any{"host": "example.org", "insecure": true}}.WithDefaults(), Env{}); err == nil {
		t.Error("unknown prop accepted")
	}
	for _, props := range []map[string]any{
		{"host": "example.org"},
		{"host": "2001:db8::1", "port": 8443, "serverName": "example.org", "minValidity": "1h"},
	} {
		if _, err := DefaultRegistry().Build(model.Check{Name: "c", Type: "tls", Props: props}.WithDefaults(), Env{}); err != nil {
			t.Errorf("%v: %v", props, err)
		}
	}
}
