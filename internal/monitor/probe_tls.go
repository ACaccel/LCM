package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
)

const (
	defaultTLSPort        = 443
	defaultTLSMinValidity = 21 * 24 * time.Hour
	minTLSMinValidity     = time.Hour
	day                   = 24 * time.Hour
)

type tlsProps struct {
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	ServerName  string         `json:"serverName"`
	MinValidity model.Duration `json:"minValidity"`
}

// validTLSName accepts a DNS name or an IP literal: no scheme, port, path or
// whitespace, which would make the dial target or the verified name ambiguous.
func validTLSName(s string) bool {
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t\r\n/@?#[]\\") {
		return false
	}
	if strings.Contains(s, ":") {
		return net.ParseIP(s) != nil
	}
	return true
}

func buildTLS(_ model.Check, p tlsProps, env Env) (Built, error) {
	if !validTLSName(p.Host) {
		return Built{}, propError("host", "must be a host name or IP address without scheme, port or path")
	}
	port := p.Port
	if port == 0 {
		port = defaultTLSPort
	}
	if port < 1 || port > 65535 {
		return Built{}, propError("port", "must be between 1 and 65535")
	}
	serverName := p.ServerName
	if serverName == "" {
		serverName = p.Host
	}
	if !validTLSName(serverName) {
		return Built{}, propError("serverName", "must be a host name or IP address")
	}
	minValidity := p.MinValidity.D()
	switch {
	case minValidity == 0:
		minValidity = defaultTLSMinValidity
	case minValidity < minTLSMinValidity:
		return Built{}, propError("minValidity", "must be at least %s", minTLSMinValidity)
	}
	clk := env.Clock
	if clk == nil {
		clk = clock.Real()
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(port))

	return Built{Probe: func(ctx context.Context) Result {
		start := time.Now()
		var d net.Dialer
		raw, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return failure(ctx, err)
		}
		defer func() { _ = raw.Close() }()
		// Verification happens below against the injected clock and roots,
		// so the handshake itself must not judge the certificate.
		conn := tls.Client(raw, &tls.Config{ServerName: serverName, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // chain and name are verified explicitly with an injectable time and roots
		if err := conn.HandshakeContext(ctx); err != nil {
			return failure(ctx, err)
		}
		latency := time.Since(start)
		certs := conn.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			return Result{Latency: latency, Message: "the server presented no certificate"}
		}
		return evaluateCertificate(certs, serverName, env.Roots, clk.Now(), minValidity, latency)
	}}, nil
}

// evaluateCertificate verifies the served chain for serverName at now and
// compares the leaf's remaining validity with minValidity.
func evaluateCertificate(certs []*x509.Certificate, serverName string, roots *x509.CertPool, now time.Time, minValidity, latency time.Duration) Result {
	leaf := certs[0]
	notAfter := leaf.NotAfter.UTC().Format("2006-01-02")
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: serverName, Roots: roots, Intermediates: inter, CurrentTime: now}); err != nil {
		return Result{Latency: latency, Message: truncate(fmt.Sprintf("certificate verification failed: %v (notAfter %s)", err, notAfter), maxMessage)}
	}
	left := leaf.NotAfter.Sub(now)
	days := int(left / day)
	if left < minValidity {
		return Result{Latency: latency, Message: fmt.Sprintf("certificate expires in %d days (notAfter %s), below the required %s", days, notAfter, formatValidity(minValidity))}
	}
	return Result{OK: true, Latency: latency, Message: fmt.Sprintf("certificate valid for %d days (notAfter %s)", days, notAfter)}
}

// formatValidity prints whole days when the duration is a multiple of one.
func formatValidity(d time.Duration) string {
	if d%day == 0 {
		return fmt.Sprintf("%d days", d/day)
	}
	return d.String()
}
