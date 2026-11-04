package xiaomi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/router/xiaomi"
	"github.com/ACaccel/lcm/internal/router/xiaomi/xiaomitest"
)

var start = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func newClient(t *testing.T, r *xiaomitest.Router, clk clock.Clock, mutate func(*xiaomi.Config)) *xiaomi.Client {
	t.Helper()
	cfg := xiaomi.Config{
		BaseURL:  r.URL(),
		Password: xiaomitest.DefaultPassword,
		Clock:    clk,
		HTTP:     r.HTTPClient(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := xiaomi.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setup(t *testing.T) (*xiaomitest.Router, *clock.Fake, *xiaomi.Client) {
	t.Helper()
	clk := clock.NewFake(start)
	r := xiaomitest.New(t, clk)
	return r, clk, newClient(t, r, clk, nil)
}

func near(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }

func TestStatusBothHashModes(t *testing.T) {
	for _, sha256 := range []bool{false, true} {
		t.Run(fmt.Sprintf("sha256=%v", sha256), func(t *testing.T) {
			r, _, c := setup(t)
			r.SetSHA256(sha256)
			st, err := c.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if r.Logins() != 1 {
				t.Fatalf("logins = %d", r.Logins())
			}
			for _, src := range []string{xiaomi.SourceStatus, xiaomi.SourceWAN} {
				if st.Sources[src].State != xiaomi.CoverageAvailable {
					t.Errorf("source %s = %+v", src, st.Sources[src])
				}
			}
			switch {
			case !near(st.CPUPercent, 12.34):
				t.Errorf("cpu = %v", st.CPUPercent)
			case st.CPUCores == nil || *st.CPUCores != 4:
				t.Errorf("cores = %v", st.CPUCores)
			case !near(st.MemoryPercent, 45):
				t.Errorf("memory = %v", st.MemoryPercent)
			case !near(st.UptimeSeconds, 86400):
				t.Errorf("uptime = %v", st.UptimeSeconds)
			case !near(st.TemperatureC, 52):
				t.Errorf("temperature = %v", st.TemperatureC)
			case st.Clients == nil || *st.Clients != 5:
				t.Errorf("clients = %v", st.Clients)
			case st.Model != "RB03" || st.Firmware != "1.0.53":
				t.Errorf("model/firmware = %q/%q", st.Model, st.Firmware)
			}
			w := st.WAN
			switch {
			case w.Link == nil || !*w.Link:
				t.Errorf("wan link = %v", w.Link)
			case w.Status == nil || *w.Status != 1:
				t.Errorf("wan status = %v", w.Status)
			case !near(w.UptimeSeconds, 86400) || w.Type != "pppoe":
				t.Errorf("wan uptime/type = %v/%q", w.UptimeSeconds, w.Type)
			case !near(w.UpBytesPerSec, 1024) || !near(w.DownBytesPerSec, 20480):
				t.Errorf("wan speed = %v/%v", w.UpBytesPerSec, w.DownBytesPerSec)
			case w.UploadedBytes == nil || *w.UploadedBytes != 123456789 || w.DownloadedBytes == nil || *w.DownloadedBytes != 987654321:
				t.Errorf("wan bytes = %v/%v", w.UploadedBytes, w.DownloadedBytes)
			}
		})
	}
}

func TestSecretsNotExposed(t *testing.T) {
	r, _, c := setup(t)
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal([]any{info, st})
	if err != nil {
		t.Fatal(err)
	}
	cfg := xiaomi.Config{BaseURL: r.URL(), Password: xiaomitest.DefaultPassword}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join([]string{
		string(js), string(cfgJSON),
		fmt.Sprintf("%v %+v %#v", info, info, info),
		fmt.Sprintf("%v %+v %#v", st, st, st),
		fmt.Sprintf("%v %+v %#v %s %d %x %q", cfg, cfg, cfg, cfg, cfg, cfg, cfg),
		fmt.Sprintf("%v %+v %#v %s %d %x %q", c, c, c, c, c, c, c),
	}, "\n")
	for _, secret := range []string{
		xiaomitest.DefaultPassword, xiaomitest.Serial, xiaomitest.RouterID,
		xiaomitest.HardwareMAC, xiaomitest.WANSecret, xiaomitest.WANAddress,
	} {
		if strings.Contains(out, secret) {
			t.Errorf("output contains %q:\n%s", secret, out)
		}
	}
}

func TestInfo(t *testing.T) {
	r, _, c := setup(t)
	r.SetSHA256(true)
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.HashMode != xiaomi.HashSHA256 || info.Model != "xiaomi.router.rb03" || info.Firmware != "1.0.53" ||
		info.Hardware != "RB03" || info.Name != "Redmi_AX6" || info.DisplayName != "Redmi Router AX6" ||
		info.Initialized == nil || !*info.Initialized || info.WANConnected == nil || !*info.WANConnected {
		t.Fatalf("info = %+v", info)
	}
	r.SetFailure(xiaomitest.InitInfo, xiaomitest.FailHTTP500)
	// The router answered: unusable, but not unreachable.
	if _, err := c.Info(context.Background()); !errors.Is(err, xiaomi.ErrUnavailable) || errors.Is(err, xiaomi.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongPasswordNotRetried(t *testing.T) {
	r, clk, c := setup(t)
	r.SetPassword("another-password")
	_, err := c.Status(context.Background())
	if !errors.Is(err, xiaomi.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), xiaomitest.DefaultPassword) {
		t.Fatal("error contains the password")
	}
	if n := r.LoginAttempts(); n != 1 {
		t.Fatalf("login attempts = %d", n)
	}
	clk.Advance(time.Second)
	if _, err := c.Reboot(context.Background()); !errors.Is(err, xiaomi.ErrAuth) {
		t.Fatalf("reboot err = %v", err)
	}
	if n := r.LoginAttempts(); n != 2 || r.Calls(xiaomitest.Reboot) != 0 {
		t.Fatalf("login attempts = %d, reboot calls = %d", n, r.Calls(xiaomitest.Reboot))
	}
}

func nonceSecond(t *testing.T, nonce string) int64 {
	t.Helper()
	parts := strings.Split(nonce, "_")
	if len(parts) != 4 || parts[0] != "0" || parts[1] != xiaomi.DefaultDeviceID {
		t.Fatalf("nonce %q", nonce)
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 0 || n > 9999 {
		t.Fatalf("nonce random %q", parts[3])
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestLoginWaitsForNextSecond(t *testing.T) {
	r, clk, c := setup(t)
	ctx := context.Background()
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	r.ExpireTokens()

	done := make(chan error, 1)
	go func() {
		_, err := c.Status(ctx)
		done <- err
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := clk.WaitForTimers(waitCtx, 1); err != nil {
		t.Fatal("client did not wait for the next second")
	}
	if n := r.LoginAttempts(); n != 1 {
		t.Fatalf("second login sent before the next second (attempts %d)", n)
	}
	clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	nonces := r.Nonces()
	if len(nonces) != 2 || r.Logins() != 2 {
		t.Fatalf("nonces %v, logins %d", nonces, r.Logins())
	}
	if a, b := nonceSecond(t, nonces[0]), nonceSecond(t, nonces[1]); a != start.Unix() || b != a+1 {
		t.Fatalf("nonce seconds %d, %d", a, b)
	}
}

func TestLoginWaitHonoursContext(t *testing.T) {
	r, clk, c := setup(t)
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.ExpireTokens()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Status(ctx)
		done <- err
	}()
	waitCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := clk.WaitForTimers(waitCtx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if clk.Pending() != 0 {
		t.Fatal("wait timer not stopped")
	}
}

func TestNonceReuseIsNotAuthError(t *testing.T) {
	// Two clients sharing a device ID in the same second: the router rejects
	// the second nonce, which is a protocol error, not a wrong password.
	r, clk, a := setup(t)
	b := newClient(t, r, clk, nil)
	if _, err := a.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := b.Status(context.Background())
	if !errors.Is(err, xiaomi.ErrUnavailable) || errors.Is(err, xiaomi.ErrAuth) || !strings.Contains(err.Error(), "1582") {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenExpiryRelogin(t *testing.T) {
	r, clk, c := setup(t)
	r.SetTokenTTL(time.Hour)
	ctx := context.Background()
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Minute)
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if n := r.Logins(); n != 1 {
		t.Fatalf("logins before expiry = %d", n)
	}
	clk.Advance(time.Hour)
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := r.Logins(); n != 2 || st.Sources[xiaomi.SourceStatus].State != xiaomi.CoverageAvailable {
		t.Fatalf("logins = %d, sources = %+v", n, st.Sources)
	}
}

func TestReloginAtMostOncePerCall(t *testing.T) {
	r, clk, c := setup(t)
	r.SetFailure(xiaomitest.Status, xiaomitest.FailInvalidToken)
	type result struct {
		st  xiaomi.Status
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := c.Status(context.Background())
		done <- result{st, err}
	}()
	// The re-login falls in the same second as the first login and waits
	// for the next one.
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clk.WaitForTimers(waitCtx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	res := <-done
	st, err := res.st, res.err
	if err != nil {
		t.Fatal(err)
	}
	if n := r.Logins(); n != 2 {
		t.Fatalf("logins = %d", n)
	}
	if cov := st.Sources[xiaomi.SourceStatus]; cov.State != xiaomi.CoverageUnavailable || cov.Reason != "code 401" {
		t.Fatalf("status source = %+v", cov)
	}
	if st.Sources[xiaomi.SourceWAN].State != xiaomi.CoverageAvailable || st.WAN.Link == nil {
		t.Fatalf("wan = %+v", st.Sources[xiaomi.SourceWAN])
	}
	if clk.Pending() != 0 {
		t.Fatal("unexpected pending timer")
	}
}

func TestConcurrentCallsShareOneLogin(t *testing.T) {
	r, _, c := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := c.Status(context.Background())
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := r.Logins(); n != 1 {
		t.Fatalf("logins = %d", n)
	}
}

func TestSourceFailures(t *testing.T) {
	cases := []struct {
		failure xiaomitest.Failure
		reason  string
	}{
		{xiaomitest.FailHTTP404, "http 404"},
		{xiaomitest.FailHTTP500, "http 500"},
		{xiaomitest.FailNonJSON, "invalid json"},
		{xiaomitest.FailCode, "code 1"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			r, _, c := setup(t)
			r.SetFailure(xiaomitest.Status, tc.failure)
			st, err := c.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if cov := st.Sources[xiaomi.SourceStatus]; cov.State != xiaomi.CoverageUnavailable || cov.Reason != tc.reason {
				t.Fatalf("status source = %+v", cov)
			}
			if st.CPUPercent != nil || st.Sources[xiaomi.SourceWAN].State != xiaomi.CoverageAvailable || !near(st.WAN.UptimeSeconds, 86400) {
				t.Fatalf("status = %+v", st)
			}
			// Model and firmware fall back to init_info.
			if st.Model != "xiaomi.router.rb03" || st.Firmware != "1.0.53" {
				t.Fatalf("model/firmware = %q/%q", st.Model, st.Firmware)
			}

			r.SetFailure(xiaomitest.WAN, tc.failure)
			st, err = c.Status(context.Background())
			if !errors.Is(err, xiaomi.ErrUnavailable) {
				t.Fatalf("all sources failing: err = %v", err)
			}
			if st.Sources[xiaomi.SourceWAN].Reason != tc.reason {
				t.Fatalf("wan source = %+v", st.Sources[xiaomi.SourceWAN])
			}
		})
	}
}

func TestSecondSourceTransportFailure(t *testing.T) {
	r, _, c := setup(t)
	r.SetFailure(xiaomitest.WAN, xiaomitest.FailDrop)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Sources[xiaomi.SourceWAN].State != xiaomi.CoverageUnavailable || st.CPUPercent == nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestUnreachableRouter(t *testing.T) {
	r, _, c := setup(t)
	r.Close()
	if _, err := c.Status(context.Background()); !errors.Is(err, xiaomi.ErrUnavailable) || !errors.Is(err, xiaomi.ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}

func TestLenientParsing(t *testing.T) {
	r, _, c := setup(t)
	r.SetStatusBody(map[string]any{
		"code":        "0",
		"cpu":         map[string]any{"core": "2", "load": "0.5"},
		"mem":         map[string]any{"usage": "0.25"},
		"upTime":      123.5,
		"temperature": "0",
		"count":       map[string]any{"online": "7"},
		"wan":         map[string]any{"upspeed": 10, "downspeed": "n/a", "upload": 42, "download": "-1"},
		"hardware":    "unexpected",
	})
	r.SetWANBody(map[string]any{"code": 0, "info": map[string]any{"link": "0", "status": "2", "uptime": "77"}})
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case !near(st.CPUPercent, 50) || st.CPUCores == nil || *st.CPUCores != 2:
		t.Errorf("cpu = %v/%v", st.CPUPercent, st.CPUCores)
	case !near(st.MemoryPercent, 25) || !near(st.UptimeSeconds, 123.5):
		t.Errorf("memory/uptime = %v/%v", st.MemoryPercent, st.UptimeSeconds)
	case st.TemperatureC != nil:
		t.Errorf("temperature 0 must be unknown, got %v", *st.TemperatureC)
	case st.Clients == nil || *st.Clients != 7:
		t.Errorf("clients = %v", st.Clients)
	case !near(st.WAN.UpBytesPerSec, 10) || st.WAN.DownBytesPerSec != nil:
		t.Errorf("speeds = %v/%v", st.WAN.UpBytesPerSec, st.WAN.DownBytesPerSec)
	case st.WAN.UploadedBytes == nil || *st.WAN.UploadedBytes != 42 || st.WAN.DownloadedBytes != nil:
		t.Errorf("bytes = %v/%v", st.WAN.UploadedBytes, st.WAN.DownloadedBytes)
	case st.WAN.Link == nil || *st.WAN.Link || st.WAN.Status == nil || *st.WAN.Status != 2 || !near(st.WAN.UptimeSeconds, 77):
		t.Errorf("wan = %+v", st.WAN)
	case st.WAN.Type != "":
		t.Errorf("wan type = %q", st.WAN.Type)
	case st.Model != "xiaomi.router.rb03":
		t.Errorf("model fallback = %q", st.Model)
	}
}

func TestReboot(t *testing.T) {
	cases := []struct {
		name     string
		behavior xiaomitest.RebootBehavior
		failure  xiaomitest.Failure
		want     xiaomi.RebootOutcome
		wantErr  error
		reboots  int
	}{
		{"accepted", xiaomitest.RebootAccept, xiaomitest.FailNone, xiaomi.RebootAccepted, nil, 1},
		{"connection dropped", xiaomitest.RebootAcceptAndDrop, xiaomitest.FailNone, xiaomi.RebootUnknown, xiaomi.ErrUnavailable, 1},
		{"refused", xiaomitest.RebootRefuse, xiaomitest.FailNone, xiaomi.RebootRejected, xiaomi.ErrRejected, 0},
		{"http 404", xiaomitest.RebootAccept, xiaomitest.FailHTTP404, xiaomi.RebootRejected, xiaomi.ErrRejected, 0},
		{"http 500", xiaomitest.RebootAccept, xiaomitest.FailHTTP500, xiaomi.RebootUnknown, xiaomi.ErrUnavailable, 0},
		{"non-json", xiaomitest.RebootAccept, xiaomitest.FailNonJSON, xiaomi.RebootUnknown, xiaomi.ErrUnavailable, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, c := setup(t)
			r.SetReboot(tc.behavior, 0)
			r.SetFailure(xiaomitest.Reboot, tc.failure)
			// A prior call leaves a keep-alive connection that net/http would
			// replay a dropped GET on; reboot must not be replayed.
			if _, err := c.Status(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := c.Reboot(context.Background())
			if got != tc.want || (tc.wantErr == nil) != (err == nil) || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("reboot = %s, %v; want %s, %v", got, err, tc.want, tc.wantErr)
			}
			if r.Reboots() != tc.reboots || r.Calls(xiaomitest.Reboot) != 1 {
				t.Fatalf("reboots = %d, reboot calls = %d", r.Reboots(), r.Calls(xiaomitest.Reboot))
			}
			for _, p := range r.Paths() {
				if strings.HasSuffix(p, "/api/xqsystem/reboot") && !strings.Contains(p, "TOKEN") {
					t.Fatalf("reboot path without token: %s", p)
				}
			}
		})
	}
}

func TestRebootNotSent(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		r, _, c := setup(t)
		r.Close()
		got, err := c.Reboot(context.Background())
		if got != xiaomi.RebootNotSent || !errors.Is(err, xiaomi.ErrUnavailable) {
			t.Fatalf("reboot = %s, %v", got, err)
		}
	})
	t.Run("context done", func(t *testing.T) {
		_, _, c := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := c.Reboot(ctx)
		if got != xiaomi.RebootNotSent || err == nil {
			t.Fatalf("reboot = %s, %v", got, err)
		}
	})
}

func TestRebootWithExpiredToken(t *testing.T) {
	r, clk, c := setup(t)
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.ExpireTokens()
	clk.Advance(time.Second)
	got, err := c.Reboot(context.Background())
	if got != xiaomi.RebootAccepted || err != nil {
		t.Fatalf("reboot = %s, %v", got, err)
	}
	if r.Logins() != 2 || r.Reboots() != 1 || r.Calls(xiaomitest.Reboot) != 2 {
		t.Fatalf("logins = %d, reboots = %d, calls = %d", r.Logins(), r.Reboots(), r.Calls(xiaomitest.Reboot))
	}
}

func TestRebootUptimeReset(t *testing.T) {
	r, clk, c := setup(t)
	r.SetReboot(xiaomitest.RebootAcceptAndDrop, 30*time.Second)
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !near(st.UptimeSeconds, 86400) {
		t.Fatalf("uptime before = %v", st.UptimeSeconds)
	}
	if got, _ := c.Reboot(ctx); got != xiaomi.RebootUnknown {
		t.Fatalf("reboot = %s", got)
	}
	if _, err := c.Status(ctx); !errors.Is(err, xiaomi.ErrUnavailable) {
		t.Fatalf("status while down: err = %v", err)
	}
	clk.Advance(45 * time.Second)
	st, err = c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !near(st.UptimeSeconds, 15) || !near(st.WAN.UptimeSeconds, 15) {
		t.Fatalf("uptime after = %v, wan %v", st.UptimeSeconds, st.WAN.UptimeSeconds)
	}
	if r.Reboots() != 1 || r.Logins() != 2 {
		t.Fatalf("reboots = %d, logins = %d", r.Reboots(), r.Logins())
	}
}

func TestLogout(t *testing.T) {
	r, clk, c := setup(t)
	ctx := context.Background()
	if err := c.Logout(ctx); err != nil || r.Calls(xiaomitest.Logout) != 0 {
		t.Fatalf("logout without session: %v, calls %d", err, r.Calls(xiaomitest.Logout))
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Logout(ctx); err != nil || r.Calls(xiaomitest.Logout) != 1 {
		t.Fatalf("logout: %v, calls %d", err, r.Calls(xiaomitest.Logout))
	}
	clk.Advance(time.Second)
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Logins() != 2 {
		t.Fatalf("logins = %d", r.Logins())
	}
}

func TestOnlyAllowlistedPaths(t *testing.T) {
	r, clk, c := setup(t)
	ctx := context.Background()
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if _, err := c.Reboot(ctx); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"/cgi-bin/luci/api/xqsystem/init_info":             true,
		"/cgi-bin/luci/api/xqsystem/login":                 true,
		"/cgi-bin/luci/;stok=TOKEN/api/misystem/status":    true,
		"/cgi-bin/luci/;stok=TOKEN/api/xqnetwork/wan_info": true,
		"/cgi-bin/luci/;stok=TOKEN/api/xqsystem/reboot":    true,
		"/cgi-bin/luci/;stok=TOKEN/web/logout":             true,
	}
	paths := r.Paths()
	if len(paths) == 0 {
		t.Fatal("no requests recorded")
	}
	for _, p := range paths {
		if !allowed[p] {
			t.Errorf("unexpected path %s", p)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []xiaomi.Config{
		{BaseURL: "ftp://192.0.2.1", Password: "p"},
		{BaseURL: "http://", Password: "p"},
		{BaseURL: "http://admin:pw@192.0.2.1", Password: "p"}, //nolint:gosec // synthetic URL checking that credentials are refused
		{BaseURL: "http://192.0.2.1/cgi-bin", Password: "p"},
		{BaseURL: "http://192.0.2.1/?a=b", Password: "p"},
		{BaseURL: "http://192.0.2.1", Password: ""},
		{BaseURL: "http://192.0.2.1", Password: "p", DeviceID: "0_x_1"},
		{BaseURL: "http://192.0.2.1", Password: "p", Timeout: -time.Second},
		{BaseURL: "http://192.0.2.1", Password: "p", Username: "admin\nx"},
	}
	for _, cfg := range bad {
		c, err := xiaomi.New(cfg)
		if err == nil {
			t.Errorf("%v: accepted", cfg)
			continue
		}
		if c != nil || strings.Contains(err.Error(), "pw@") {
			t.Errorf("%v: %v", cfg, err)
		}
	}
	for _, u := range []string{"http://192.168.31.1", "https://router.lan:8443/", "http://[fd00::1]"} {
		if _, err := xiaomi.New(xiaomi.Config{BaseURL: u, Password: "p", InsecureSkipVerify: true}); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}
