package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/config"
)

func TestStartRunAndClose(t *testing.T) {
	defer goleak.VerifyNone(t)
	h := newHarness(t)
	stop := h.run()

	resp, err := http.Get("http://" + h.app.Addr().String() + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Status string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK || body.Status == "" {
		t.Fatalf("health: status %d body %+v err %v", resp.StatusCode, body, err)
	}
	_ = resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()

	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := h.app.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := h.app.DB.Ping(ctx); err == nil {
		t.Error("database still open after Close")
	}
	if err := h.app.Run(ctx); err == nil {
		t.Error("Run after Close succeeded")
	}
}

func TestCloseWithoutRun(t *testing.T) {
	defer goleak.VerifyNone(t)
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := h.app.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunTwiceFails(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.app.Run(ctx); err != nil {
		t.Fatalf("Run with a canceled context: %v", err)
	}
	if err := h.app.Run(ctx); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("second Run: %v", err)
	}
}

func TestNewCreatesServeDirectories(t *testing.T) {
	h := newHarness(t)
	for _, sub := range []string{"secrets", "calendars"} {
		if st, err := os.Stat(filepath.Join(h.dir, sub)); err != nil || !st.IsDir() {
			t.Errorf("%s: %v", sub, err)
		}
	}
	if st, err := os.Stat(filepath.Join(h.dir, "secrets")); err == nil && st.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir mode = %o, want 700", st.Mode().Perm())
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "d")
	cfg.LockTTL = 0
	if _, err := app.New(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "lock ttl") {
		t.Fatalf("err = %v", err)
	}
}

// A port that is already taken must fail New before it touches the database
// or any service.
func TestNewFailsWhenAddressTaken(t *testing.T) {
	first := newHarness(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "d2")
	cfg.Listen = first.app.Addr().String()
	_, err := app.New(context.Background(), cfg, app.WithLogger(testLogger(t)))
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.DataDir, config.DBFileName)); statErr == nil {
		t.Error("database was created although the port was taken")
	}
}
