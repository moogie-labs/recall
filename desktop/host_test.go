package main

import (
	"context"
	"errors"
	"slices"
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

func TestUpgradeOpensInstallerBeforeClosingAndQuitting(t *testing.T) {
	var order []string
	var token string
	h := newHost(func(_ string, payload any) {
		if r, ok := payload.(quitRequest); ok {
			token = r.Token
		}
	}, func() { order = append(order, "quit") }, func(string) {})
	h.close = func() error { order = append(order, "close"); return nil }
	h.install = func(pkg string) error { order = append(order, "install "+pkg); return nil }
	if h.InstallOnQuit("/cache/a.pkg") == nil {
		t.Fatal("started an upgrade quit before the renderer was ready")
	}
	h.Ready()
	if err := h.InstallOnQuit("/cache/a.pkg"); err != nil || token == "" {
		t.Fatal(err, token)
	}
	if r := h.CompleteQuit(token); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	if !slices.Equal(order, []string{"install /cache/a.pkg", "close", "quit"}) {
		t.Fatal(order)
	}
}

func TestInstallerFailureKeepsRecallRunning(t *testing.T) {
	var token string
	var cancelled []string
	h := newHost(func(_ string, payload any) {
		switch p := payload.(type) {
		case quitRequest:
			token = p.Token
		case upgradeCancelled:
			cancelled = append(cancelled, p.Problem.Message)
		}
	}, func() { t.Fatal("quit after Installer failed") }, func(string) {})
	h.close = func() error { t.Fatal("closed the collection after Installer failed"); return nil }
	h.install = func(string) error { return errors.New("open failed") }
	h.Ready()
	if err := h.InstallOnQuit("/cache/a.pkg"); err != nil {
		t.Fatal(err)
	}
	if r := h.CompleteQuit(token); r.Problem == nil || r.Problem.Message != "Could not open Installer for the upgrade" {
		t.Fatal(r.Problem)
	}
	h.CancelQuit(token) // the renderer cancels after a failed CompleteQuit
	if !slices.Equal(cancelled, []string{"Could not open Installer for the upgrade"}) {
		t.Fatal(cancelled)
	}
}

func TestCancelledQuitAbandonsTheUpgrade(t *testing.T) {
	var tokens, cancelled []string
	quits, installs := 0, 0
	h := newHost(func(_ string, payload any) {
		switch p := payload.(type) {
		case quitRequest:
			tokens = append(tokens, p.Token)
		case upgradeCancelled:
			cancelled = append(cancelled, p.Problem.Message)
		}
	}, func() { quits++ }, func(string) {})
	h.install = func(string) error { installs++; return nil }
	h.Ready()
	h.InstallOnQuit("/cache/a.pkg")
	h.CancelQuit(tokens[0]) // a draft could not be saved
	if !slices.Equal(cancelled, []string{"Upgrade cancelled: unsaved changes could not be saved."}) {
		t.Fatal(cancelled)
	}
	// A later ordinary quit must not open the abandoned package.
	h.BeforeClose(context.Background())
	if r := h.CompleteQuit(tokens[1]); r.Problem != nil || installs != 0 || quits != 1 {
		t.Fatal(r.Problem, installs, quits)
	}
}
