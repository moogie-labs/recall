package main

import (
	"context"
	"testing"
)

func TestBeforeCloseRequiresHandshake(t *testing.T) {
	var tokens []string
	quits := 0
	h := newHost(func(name string, payload any) { tokens = append(tokens, payload.(quitRequest).Token) }, func() { quits++ }, func(string) {})
	if h.BeforeClose(context.Background()) {
		t.Fatal("pre-ready close must not deadlock")
	}
	h.Ready()
	if !h.BeforeClose(context.Background()) || !h.BeforeClose(context.Background()) || len(tokens) != 1 {
		t.Fatal("close not coalesced/cancelled")
	}
	if h.CompleteQuit("stale").Problem == nil || quits != 0 {
		t.Fatal("stale acknowledgement accepted")
	}
	if h.CompleteQuit(tokens[0]).Problem != nil || quits != 1 {
		t.Fatal("valid acknowledgement not accepted")
	}
	if h.BeforeClose(context.Background()) {
		t.Fatal("acknowledged close cancelled")
	}
	h.CompleteQuit(tokens[0])
	if quits != 1 {
		t.Fatal("quit repeated")
	}
}

func TestExternalURLsAreValidated(t *testing.T) {
	var opened []string
	h := newHost(func(string, any) {}, func() {}, func(url string) { opened = append(opened, url) })
	for _, url := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,no", "recall:delete", "https://", "//example.com"} {
		if h.OpenExternal(url).Problem == nil {
			t.Fatalf("allowed %q", url)
		}
	}
	if h.OpenExternal("https://example.com/read?q=test").Problem != nil || len(opened) != 1 {
		t.Fatal("http URL rejected")
	}
}
func TestNativeDraftCopy(t *testing.T) {
	h := newHost(func(string, any) {}, func() {}, func(string) {})
	var copied string
	h.copy = func(text string) error { copied = text; return nil }
	if h.CopyDraft("unsaved 自由").Problem != nil || copied != "unsaved 自由" {
		t.Fatal("draft not copied through native clipboard boundary")
	}
}
