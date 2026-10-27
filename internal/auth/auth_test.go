package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

const testPassword = "correct horse battery"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newService(t *testing.T) (*auth.Service, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc, err := auth.NewService(storetest.Open(t), time.Hour, auth.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	return svc, clock
}

func TestHashAndVerify(t *testing.T) {
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC string: %q", hash)
	}
	if strings.Contains(hash, testPassword) {
		t.Fatal("hash contains the password")
	}
	ok, err := auth.VerifyPassword(testPassword, hash)
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = auth.VerifyPassword("wrong password!", hash)
	if err != nil || ok {
		t.Fatalf("verify wrong password: ok=%v err=%v", ok, err)
	}
	other, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Fatal("two hashes of the same password are identical: salt is not random")
	}
}

func TestVerifyMalformedHash(t *testing.T) {
	for _, h := range []string{
		"", "plain", "$bcrypt$x$y$z$w",
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$AAAA",
		"$argon2id$v=19$m=999999999,t=3,p=2$AAAA$AAAA",
		"$argon2id$v=18$m=65536,t=3,p=2$AAAA$AAAA",
	} {
		if ok, err := auth.VerifyPassword("x", h); err == nil || ok {
			t.Errorf("VerifyPassword(%q) = %v, %v; want error", h, ok, err)
		}
	}
}

func TestSetPasswordValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SetPassword(ctx, "admin", "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Errorf("short password: %v", err)
	}
	if _, err := svc.SetPassword(ctx, "", testPassword); !errors.Is(err, auth.ErrInvalidUsername) {
		t.Errorf("empty username: %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	created, err := svc.SetPassword(ctx, "admin", testPassword)
	if err != nil || !created {
		t.Fatalf("SetPassword: created=%v err=%v", created, err)
	}

	token, _, user, err := svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" || token == "" {
		t.Fatalf("user=%+v token=%q", user, token)
	}

	if _, _, _, err := svc.Login(ctx, "admin", "wrong password!!"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "nobody", testPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestSessionExpiryWithInjectedClock(t *testing.T) {
	svc, clock := newService(t)
	ctx := context.Background()
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, expires, _, err := svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Now().Add(time.Hour); !expires.Equal(want) {
		t.Errorf("expires = %v, want %v", expires, want)
	}

	clock.Advance(59 * time.Minute)
	if u, err := svc.Session(ctx, token); err != nil || u.Username != "admin" {
		t.Fatalf("session before expiry: %+v %v", u, err)
	}
	clock.Advance(time.Minute)
	if _, err := svc.Session(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session at expiry: %v", err)
	}
	if _, err := svc.Session(ctx, ""); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("empty token: %v", err)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("session after logout: %v", err)
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := svc.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.SetPassword(ctx, "admin", "another long password")
	if err != nil || created {
		t.Fatalf("update: created=%v err=%v", created, err)
	}
	if _, err := svc.Session(ctx, token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old session survived password change: %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "admin", testPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "admin", "another long password"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestTokenLifecycle(t *testing.T) {
	svc, clock := newService(t)
	ctx := context.Background()

	tok, secret, err := svc.CreateToken(ctx, "  ci  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "ci" || !strings.HasPrefix(secret, "lcm_") || len(secret) != len("lcm_")+43 {
		t.Fatalf("token=%+v secret=%q", tok, secret)
	}

	actor, err := svc.VerifyToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Kind != auth.KindToken || actor.Name != "ci" || actor.ID == "" {
		t.Fatalf("actor = %+v", actor)
	}

	list, err := svc.ListTokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	firstUse := list[0].LastUsedAt
	if firstUse == nil || !firstUse.Equal(clock.Now()) {
		t.Fatalf("lastUsedAt = %v, want %v", firstUse, clock.Now())
	}

	// Use within the granularity window must not rewrite the timestamp.
	clock.Advance(10 * time.Second)
	if _, err := svc.VerifyToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.ListTokens(ctx)
	if !list[0].LastUsedAt.Equal(*firstUse) {
		t.Errorf("lastUsedAt updated within one minute: %v", list[0].LastUsedAt)
	}
	clock.Advance(2 * time.Minute)
	if _, err := svc.VerifyToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.ListTokens(ctx)
	if !list[0].LastUsedAt.Equal(clock.Now()) {
		t.Errorf("lastUsedAt = %v, want %v", list[0].LastUsedAt, clock.Now())
	}

	if err := svc.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyToken(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked token verified: %v", err)
	}
	list, _ = svc.ListTokens(ctx)
	revokedAt := list[0].RevokedAt
	if revokedAt == nil {
		t.Fatal("revokedAt not set")
	}
	clock.Advance(time.Hour)
	if err := svc.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	list, _ = svc.ListTokens(ctx)
	if !list[0].RevokedAt.Equal(*revokedAt) {
		t.Error("second revoke changed revokedAt")
	}
	if err := svc.RevokeToken(ctx, 9999); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
}

func TestTokenScope(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	def, defSecret, err := svc.CreateToken(ctx, "default", "")
	if err != nil || def.Scope != auth.ScopeAdmin {
		t.Fatalf("default token = %+v, %v", def, err)
	}
	ing, ingSecret, err := svc.CreateToken(ctx, "hook", auth.ScopeIngest)
	if err != nil || ing.Scope != auth.ScopeIngest {
		t.Fatalf("ingest token = %+v, %v", ing, err)
	}
	if _, _, err := svc.CreateToken(ctx, "bad", "root"); !errors.Is(err, auth.ErrInvalidScope) {
		t.Fatalf("invalid scope: %v", err)
	}

	for secret, want := range map[string]auth.Scope{defSecret: auth.ScopeAdmin, ingSecret: auth.ScopeIngest} {
		if _, got, err := svc.VerifyTokenScope(ctx, secret); err != nil || got != want {
			t.Errorf("VerifyTokenScope = %q, %v; want %q", got, err, want)
		}
	}
	list, err := svc.ListTokens(ctx)
	if err != nil || len(list) != 2 || list[0].Scope != auth.ScopeIngest || list[1].Scope != auth.ScopeAdmin {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestVerifyTokenRejectsGarbage(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	for _, s := range []string{"", "nope", "lcm_", "lcm_AAAAAAAA"} {
		if _, err := svc.VerifyToken(ctx, s); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("VerifyToken(%q) = %v", s, err)
		}
	}
}

func TestCreateTokenNameValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	for _, n := range []string{"", "   ", strings.Repeat("x", 65)} {
		if _, _, err := svc.CreateToken(ctx, n, ""); !errors.Is(err, auth.ErrInvalidName) {
			t.Errorf("CreateToken(%q) = %v", n, err)
		}
	}
}

func TestActorContext(t *testing.T) {
	if _, ok := auth.ActorFrom(context.Background()); ok {
		t.Fatal("empty context has an actor")
	}
	ctx := auth.WithActor(context.Background(), auth.CLI)
	got, ok := auth.ActorFrom(ctx)
	if !ok || got != auth.CLI {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if !auth.KindAI.Valid() || auth.Kind("root").Valid() {
		t.Fatal("Kind.Valid")
	}
}
