package varrefs

import (
	"context"
	"errors"
	"testing"
)

func newSeededLookup(t *testing.T) *MemoryLookup {
	t.Helper()
	m := NewMemoryLookup()
	m.SetServiceName("p1", "env_prod", "cache", "svc_cache")
	m.SetServiceName("p1", "env_prod", "db", "svc_db")
	m.SetService("p1", "env_prod", "svc_cache", "HOST", "10.0.0.5")
	m.SetService("p1", "env_prod", "svc_cache", "PORT", "6379")
	m.SetService("p1", "env_prod", "svc_db", "DATABASE_URL", "postgres://${{LOG_PREFIX}}@${{cache.HOST}}:${{cache.PORT}}/db")
	m.SetService("p1", "env_prod", "svc_db", "LOG_PREFIX", "primary")
	m.SetShared("p1", "env_prod", "SENTRY_DSN", "https://sentry.example/abc")
	return m
}

func TestResolveServiceVar(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	got, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "DATABASE_URL", "postgres://${{cache.HOST}}:${{cache.PORT}}/db")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "postgres://10.0.0.5:6379/db"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveChainedLocalRef(t *testing.T) {
	m := newSeededLookup(t)
	r := NewResolver(m)
	got, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "DATABASE_URL", "postgres://${{LOG_PREFIX}}@${{cache.HOST}}:${{cache.PORT}}/db")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "postgres://primary@10.0.0.5:6379/db"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveSharedRef(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	got, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "FOO", "obs=${{shared.SENTRY_DSN}}")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "obs=https://sentry.example/abc" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBuiltinDefaults(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	got, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "https://${{this.MIDNIGHT_PRIVATE_DOMAIN}}/api")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "https://svc_db.midnight.internal/api" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBuiltinWithExplicitValue(t *testing.T) {
	m := newSeededLookup(t)
	m.SetBuiltin("p1", "env_prod", "svc_db", BuiltinPublicDomain, "db.example.com")
	r := NewResolver(m)
	got, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "https://${{this.MIDNIGHT_PUBLIC_DOMAIN}}/api")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "https://db.example.com/api" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveUnknownService(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "${{ghost.HOST}}")
	var unk ErrUnknownReference
	if !errors.As(err, &unk) {
		t.Fatalf("err = %v, want ErrUnknownReference", err)
	}
	if unk.Kind != RefServiceVar || unk.Service != "ghost" || unk.Name != "HOST" {
		t.Fatalf("err = %#v", unk)
	}
}

func TestResolveUnknownLocal(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "${{MISSING}}")
	var unk ErrUnknownReference
	if !errors.As(err, &unk) || unk.Kind != RefLocal || unk.Name != "MISSING" {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveUnknownShared(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "${{shared.MISSING}}")
	var unk ErrUnknownReference
	if !errors.As(err, &unk) || unk.Kind != RefShared || unk.Name != "MISSING" {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveUnknownBuiltin(t *testing.T) {
	r := NewResolver(newSeededLookup(t))
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_db", "URL", "${{this.NOT_REAL}}")
	var unk ErrUnknownReference
	if !errors.As(err, &unk) || unk.Kind != RefBuiltin || unk.Name != "NOT_REAL" {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCycleSelfReference(t *testing.T) {
	m := NewMemoryLookup()
	m.SetService("p1", "env_prod", "svc_a", "X", "${{X}}")
	r := NewResolver(m)
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_a", "X", "${{X}}")
	var cyc ErrReferenceCycle
	if !errors.As(err, &cyc) {
		t.Fatalf("err = %v, want ErrReferenceCycle", err)
	}
	if len(cyc.Path) < 2 {
		t.Fatalf("path = %v", cyc.Path)
	}
}

func TestResolveCycleTwoHop(t *testing.T) {
	m := NewMemoryLookup()
	m.SetService("p1", "env_prod", "svc_a", "X", "${{Y}}")
	m.SetService("p1", "env_prod", "svc_a", "Y", "${{X}}")
	r := NewResolver(m)
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_a", "X", "${{Y}}")
	var cyc ErrReferenceCycle
	if !errors.As(err, &cyc) {
		t.Fatalf("err = %v, want ErrReferenceCycle", err)
	}
	if cyc.Path[0] != "svc_a.X" {
		t.Fatalf("path = %v", cyc.Path)
	}
}

func TestResolveCrossServiceCycle(t *testing.T) {
	m := NewMemoryLookup()
	m.SetServiceName("p1", "env_prod", "a", "svc_a")
	m.SetServiceName("p1", "env_prod", "b", "svc_b")
	m.SetService("p1", "env_prod", "svc_a", "URL", "${{b.URL}}")
	m.SetService("p1", "env_prod", "svc_b", "URL", "${{a.URL}}")
	r := NewResolver(m)
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc_a", "URL", "${{b.URL}}")
	var cyc ErrReferenceCycle
	if !errors.As(err, &cyc) {
		t.Fatalf("err = %v, want ErrReferenceCycle", err)
	}
}

func TestResolveServiceFullMap(t *testing.T) {
	m := NewMemoryLookup()
	m.SetServiceName("p1", "env_prod", "cache", "svc_cache")
	m.SetService("p1", "env_prod", "svc_cache", "HOST", "10.0.0.5")
	m.SetService("p1", "env_prod", "svc_cache", "PORT", "6379")
	m.SetService("p1", "env_prod", "svc_app", "URL", "redis://${{cache.HOST}}:${{cache.PORT}}")
	m.SetService("p1", "env_prod", "svc_app", "NAME", "app")
	r := NewResolver(m)
	got, err := r.Resolve(context.Background(), "p1", "env_prod", "svc_app")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["URL"] != "redis://10.0.0.5:6379" || got["NAME"] != "app" {
		t.Fatalf("got = %#v", got)
	}
	if got[BuiltinPrivateDomain] != "svc_app.midnight.internal" {
		t.Fatalf("private domain = %q", got[BuiltinPrivateDomain])
	}
	if got[BuiltinServiceName] != "svc_app" {
		t.Fatalf("service name = %q", got[BuiltinServiceName])
	}
}

func TestResolveInvalidSyntaxPropagates(t *testing.T) {
	r := NewResolver(NewMemoryLookup())
	_, err := r.Render(context.Background(), "p1", "env_prod", "svc", "X", "${{ unfinished")
	var syn ErrInvalidSyntax
	if !errors.As(err, &syn) {
		t.Fatalf("err = %v, want ErrInvalidSyntax", err)
	}
}
