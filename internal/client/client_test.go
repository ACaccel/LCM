package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/client"
)

func TestNewValidatesTheServerURL(t *testing.T) {
	for _, bad := range []string{"", "127.0.0.1:7800", "ftp://host", "http://", "http://h/?x=1", "not a url"} {
		if _, err := client.New(bad, "tok", nil); err == nil {
			t.Errorf("New(%q) succeeded", bad)
		}
	}
	if _, err := client.New("http://127.0.0.1:7800/", "", nil); err == nil {
		t.Error("an empty token was accepted")
	}
	if _, err := client.New("https://lcm.example.com/prefix/", "tok", nil); err != nil {
		t.Error(err)
	}
}

func TestPathEscapesSegments(t *testing.T) {
	if got := client.Path("services", "a b/c"); got != "/services/a%20b%2Fc" {
		t.Fatalf("Path = %q", got)
	}
}

func TestDoSendsTokenAndBody(t *testing.T) {
	var got struct {
		method, path, query, auth, ct, body string
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.path, got.query, got.auth, got.ct, got.body = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c, err := client.New(ts.URL+"/", "secret-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(context.Background(), client.Request{
		Method: "POST", Path: client.Path("apply"), Query: url.Values{"dryRun": {"true"}}, Body: []byte("a: 1"), ContentType: "application/yaml",
	})
	if err != nil || resp.Status != 200 || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if got.method != "POST" || got.path != "/api/v1/apply" || got.query != "dryRun=true" || got.auth != "Bearer secret-token" ||
		got.ct != "application/yaml" || got.body != "a: 1" {
		t.Fatalf("server saw %+v", got)
	}

	var out struct{ OK bool }
	if err := c.JSON(context.Background(), "GET", "/health", nil, nil, &out); err != nil || !out.OK {
		t.Fatalf("JSON = %+v, %v", out, err)
	}
}

func TestErrorsCarryTheEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/invalid":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"validation_failed","message":"validation failed","details":{"issues":[{"path":"spec.host","message":"bad"}]}}}`))
		case "/api/v1/plain":
			http.Error(w, "gateway trouble", http.StatusBadGateway)
		case "/api/v1/empty":
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer ts.Close()
	c, _ := client.New(ts.URL, "tok", nil)

	_, err := c.Do(context.Background(), client.Request{Method: "GET", Path: "/invalid"})
	var ae *client.Error
	if !errors.As(err, &ae) || ae.Status != 422 || ae.Code != "validation_failed" || len(ae.Issues()) != 1 || ae.Issues()[0].Path != "spec.host" ||
		!strings.Contains(ae.Error(), "validation failed (validation_failed, HTTP 422)") {
		t.Fatalf("error = %#v", err)
	}
	if _, err := c.Do(context.Background(), client.Request{Method: "GET", Path: "/plain"}); err == nil || !strings.Contains(err.Error(), "gateway trouble") {
		t.Fatalf("plain error = %v", err)
	}
	if _, err := c.Do(context.Background(), client.Request{Method: "GET", Path: "/empty"}); err == nil || !strings.Contains(err.Error(), "I'm a teapot") {
		t.Fatalf("empty error = %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var other int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other++
	}))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	c, _ := client.New(ts.URL, "tok", nil)
	if _, err := c.Do(context.Background(), client.Request{Method: "GET", Path: "/x"}); err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("err = %v", err)
	}
	if other != 0 {
		t.Fatal("the redirect target received a request, and with it the token")
	}
}

func TestUnreachableServer(t *testing.T) {
	c, _ := client.New("http://127.0.0.1:1", "tok", nil)
	if _, err := c.Do(context.Background(), client.Request{Method: "GET", Path: "/x"}); err == nil || !strings.Contains(err.Error(), "cannot reach the server") {
		t.Fatalf("err = %v", err)
	}
}
