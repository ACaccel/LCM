package secret_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/secret"
)

func write(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func noEnv(string) (string, bool) { return "", false }

func TestResolveFromFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "github-token", "s3cret\n", 0o600)
	r := secret.New(dir, secret.WithLookupEnv(noEnv))
	v, err := r.Get("github-token")
	if err != nil || v.Reveal() != "s3cret" {
		t.Fatalf("Get = %q, %v", v.Reveal(), err)
	}
}

func TestResolveFromEnvAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"LCM_SECRET_DISCORD_WEBHOOK": "from-env", "LCM_SECRET_BOTH": "env"}
	r := secret.New(dir, secret.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }))
	if v, err := r.Get("discord.webhook"); err != nil || v.Reveal() != "from-env" {
		t.Fatalf("env secret = %q, %v", v.Reveal(), err)
	}
	write(t, dir, "both", "file", 0o600)
	if v, _ := r.Get("both"); v.Reveal() != "file" {
		t.Fatalf("file must win over env, got %q", v.Reveal())
	}
}

func TestNotFoundAndInvalid(t *testing.T) {
	r := secret.New("", secret.WithLookupEnv(noEnv))
	if _, err := r.Get("missing"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	for _, name := range []string{"", "../etc/passwd", "a/b", ".hidden", "a b", strings.Repeat("a", 65)} {
		if _, err := r.Get(name); !errors.Is(err, secret.ErrInvalidName) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}

func TestPermissionWarningAndStrict(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "loose", "value", 0o644)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	r := secret.New(dir, secret.WithLookupEnv(noEnv), secret.WithLogger(log))
	for range 2 {
		if v, err := r.Get("loose"); err != nil || v.Reveal() != "value" {
			t.Fatalf("Get = %q, %v", v.Reveal(), err)
		}
	}
	if n := strings.Count(buf.String(), "accessible by other users"); n != 1 {
		t.Fatalf("warned %d times, want once: %s", n, buf.String())
	}
	if strings.Contains(buf.String(), "value") {
		t.Fatalf("log leaks the secret: %s", buf.String())
	}

	strict := secret.New(dir, secret.WithLookupEnv(noEnv), secret.WithStrict())
	if _, err := strict.Get("loose"); err == nil || strings.Contains(err.Error(), "value") {
		t.Fatalf("strict: %v", err)
	}
}

func TestEmptyAndDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "empty", "\n", 0o600)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := secret.New(dir, secret.WithLookupEnv(noEnv))
	if _, err := r.Get("empty"); err == nil {
		t.Fatal("empty secret accepted")
	}
	if _, err := r.Get("sub"); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestValueNeverPrints(t *testing.T) {
	v := secret.NewValue("hunter2")
	b, _ := json.Marshal(map[string]any{"v": v})
	out := fmt.Sprintf("%v %+v %#v %s %s", v, v, v, v, b)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("value leaked: %s", out)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "v", v)
	if strings.Contains(buf.String(), "hunter2") {
		t.Fatalf("slog leaked: %s", buf.String())
	}
}
