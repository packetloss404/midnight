package router

import "testing"

func TestRenderedConfigHashIsStableSHA256Hex(t *testing.T) {
	got := RenderedConfigHash("http:\n  routers: {}\n")
	want := "45a70e055604376f93d8b51a82e2dc2d1fe3c79e2c81541a2409884888aafd01"
	if got != want {
		t.Fatalf("RenderedConfigHash = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("hash length = %d, want 64", len(got))
	}
}
