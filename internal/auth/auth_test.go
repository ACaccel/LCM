package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
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
	db := storetest.Open(t)
	svc, err := auth.NewService(db, audit.New(db), time.Hour, auth.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	return svc, clock
}

// Every change and its audit entry commit together: when the entry cannot
// be written, the change does not happen either.
func TestChangesCommitWithTheirAuditEntry(t *testing.T) {
	db := storetest.Open(t)
	log := audit.New(db)
	svc, err := auth.NewService(db, log, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := svc.Login(ctx, "admin", testPassword, map[string]any{"ip": "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := svc.CreateToken(ctx, "ci", auth.ScopeIngest)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	entries, err := log.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Action+"/"+e.Actor.Name+"/"+e.Target)
	}
	want := "auth.logout/cli/,token.revoke/cli/token:1,token.create/cli/token:1,auth.login/admin/,admin.set_password/cli/user:admin"
	if strings.Join(got, ",") != want {
		t.Fatalf("entries = %v", got)
	}
	if entries[3].Details["ip"] != "192.0.2.1" || entries[2].Details["scope"] != "ingest" || entries[4].Details["created"] != true {
		t.Fatalf("details = %+v", entries)
	}

	// From here on no audit entry can be written.
	session, _, _, err := svc.Login(ctx, "admin", testPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	keep, keepSecret, err := svc.CreateToken(ctx, "keep", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	storetest.FailInserts(t, db, "audit_log")
	if _, err := svc.SetPassword(ctx, "admin", "a different password"); err == nil {
		t.Fatal("password changed without its audit entry")
	}
	if _, err := svc.SetPassword(ctx, "other", testPassword); err == nil {
		t.Fatal("user created without its audit entry")
	}
	if _, _, _, err := svc.Login(ctx, "admin", testPassword, nil); err == nil {
		t.Fatal("session opened without its audit entry")
	}
	if _, _, err := svc.CreateToken(ctx, "lost", auth.ScopeAdmin); err == nil {
		t.Fatal("token created without its audit entry")
	}
	if err := svc.RevokeToken(ctx, keep.ID); err == nil {
		t.Fatal("token revoked without its audit entry")
	}
	if err := svc.Logout(ctx, session); err == nil {
		t.Fatal("session ended without its audit entry")
	}
	if _, err := svc.Session(ctx, session); err != nil {
		t.Fatalf("session after failed changes: %v", err)
	}
	if _, err := svc.VerifyToken(ctx, keepSecret); err != nil {
		t.Fatalf("token after failed revoke: %v", err)
	}
	if tokens, _ := svc.ListTokens(ctx); len(tokens) != 2 {
		t.Fatalf("tokens = %+v", tokens)
	}
}

// Token management is refused to API tokens even if a route forgot to
// require a session; changes without any actor are refused as unaudited.
func TestTokenActorCannotManageTokens(t *testing.T) {
	svc, _ := newService(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
	tok, _, err := svc.CreateToken(ctx, "ci", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	asToken := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindToken, ID: "1", Name: "ci"})
	if _, _, err := svc.CreateToken(asToken, "more", auth.ScopeAdmin); !errors.Is(err, auth.ErrTokenActor) {
		t.Fatalf("token minted a token: %v", err)
	}
	if err := svc.RevokeToken(asToken, tok.ID); !errors.Is(err, auth.ErrTokenActor) {
		t.Fatalf("token revoked a token: %v", err)
	}
	if _, _, err := svc.CreateToken(context.Background(), "anonymous", auth.ScopeAdmin); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("token created without an actor: %v", err)
	}
	if tokens, _ := svc.ListTokens(ctx); len(tokens) != 1 || tokens[0].RevokedAt != nil {
		t.Fatalf("tokens = %+v", tokens)
	}
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
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if _, err := svc.SetPassword(ctx, "admin", "seven77"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Errorf("7-character password: %v", err)
	}
	if _, err := svc.SetPassword(ctx, "admin", "eight888"); err != nil {
		t.Errorf("8-character password: %v", err)
	}
	if _, err := svc.SetPassword(ctx, "", testPassword); !errors.Is(err, auth.ErrInvalidUsername) {
		t.Errorf("empty username: %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, _ := newService(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
	created, err := svc.SetPassword(ctx, "admin", testPassword)
	if err != nil || !created {
		t.Fatalf("SetPassword: created=%v err=%v", created, err)
	}

	token, _, user, err := svc.Login(ctx, "admin", testPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" || token == "" {
		t.Fatalf("user=%+v token=%q", user, token)
	}

	if _, _, _, err := svc.Login(ctx, "admin", "wrong password!!", nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "nobody", testPassword, nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestSessionExpiryWithInjectedClock(t *testing.T) {
	svc, clock := newService(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, expires, _, err := svc.Login(ctx, "admin", testPassword, nil)
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
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := svc.Login(ctx, "admin", testPassword, nil)
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
	ctx := auth.WithActor(context.Background(), auth.CLI)
	if _, err := svc.SetPassword(ctx, "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := svc.Login(ctx, "admin", testPassword, nil)
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
	if _, _, _, err := svc.Login(ctx, "admin", testPassword, nil); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "admin", "another long password", nil); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestTokenLifecycle(t *testing.T) {
	svc, clock := newService(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)

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
	ctx := auth.WithActor(context.Background(), auth.CLI)

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
	ctx := auth.WithActor(context.Background(), auth.CLI)
	for _, s := range []string{"", "nope", "lcm_", "lcm_AAAAAAAA"} {
		if _, err := svc.VerifyToken(ctx, s); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("VerifyToken(%q) = %v", s, err)
		}
	}
}

func TestCreateTokenNameValidation(t *testing.T) {
	svc, _ := newService(t)
	ctx := auth.WithActor(context.Background(), auth.CLI)
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
