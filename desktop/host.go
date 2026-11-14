package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	dto "recall/internal/desktop"
	"sync"
)

type quitRequest struct {
	Token string `json:"token"`
}
type upgradeCancelled struct {
	Problem dto.Problem `json:"problem"`
}

const (
	installFailed    = "Could not open Installer for the upgrade"
	upgradeAbandoned = "Upgrade cancelled: unsaved changes could not be saved."
)

type Host struct {
	mu             sync.Mutex
	ready, allowed bool
	completing     bool
	token          string
	emit           func(string, any)
	quit           func()
	browse         func(string)
	choose         func() (string, error)
	close          func() error
	copy           func(string) error
	install        func(string) error // opens a verified package in Installer
	pending        string             // package to open when the quit completes
	installProblem string
}

func newHost(emit func(string, any), quit func(), browse func(string)) *Host {
	return &Host{emit: emit, quit: quit, browse: browse}
}
func (h *Host) Ready() { h.mu.Lock(); h.ready = true; h.mu.Unlock() }
func (h *Host) CopyDraft(text string) dto.OperationReply {
	if h.copy == nil {
		return dto.OperationReply{Problem: &dto.Problem{Code: "runtime_unavailable", Message: "Native clipboard is unavailable"}}
	}
	if err := h.copy(text); err != nil {
		return dto.OperationReply{Problem: &dto.Problem{Code: "internal", Message: "Could not copy the draft"}}
	}
	return dto.OperationReply{}
}
func (h *Host) BeforeClose(ctx context.Context) bool {
	h.mu.Lock()
	if !h.ready || h.allowed {
		h.mu.Unlock()
		return false
	}
	if h.token != "" {
		h.mu.Unlock()
		return true
	}
	var b [16]byte
	rand.Read(b[:])
	h.token = hex.EncodeToString(b[:])
	token := h.token
	h.mu.Unlock()
	h.emit("desktop:quit-requested", quitRequest{token})
	return true
}

// InstallOnQuit records a verified installer package and starts the normal
// draft-protected quit; CompleteQuit opens the package just before exiting.
func (h *Host) InstallOnQuit(pkg string) error {
	h.mu.Lock()
	if !h.ready || h.allowed || h.completing || h.install == nil {
		h.mu.Unlock()
		return errors.New("the app cannot quit to install the upgrade now")
	}
	h.pending = pkg
	h.mu.Unlock()
	h.BeforeClose(context.Background())
	return nil
}
func (h *Host) CompleteQuit(token string) dto.OperationReply {
	h.mu.Lock()
	if h.allowed {
		h.mu.Unlock()
		return dto.OperationReply{}
	}
	if token == "" || token != h.token {
		h.mu.Unlock()
		return dto.OperationReply{Problem: &dto.Problem{Code: "invalid_argument", Message: "Invalid quit request"}}
	}
	if h.completing {
		h.mu.Unlock()
		return dto.OperationReply{}
	}
	h.completing = true
	pending := h.pending
	h.mu.Unlock()
	// Open Installer before closing anything, so a failure leaves Recall running.
	if pending != "" {
		if err := h.install(pending); err != nil {
			h.mu.Lock()
			h.completing = false
			h.installProblem = installFailed
			h.mu.Unlock()
			return dto.OperationReply{Problem: &dto.Problem{Code: "runtime_unavailable", Message: installFailed}}
		}
		h.mu.Lock()
		h.pending = ""
		h.mu.Unlock()
	}
	if h.close != nil {
		if err := h.close(); err != nil {
			h.mu.Lock()
			h.completing = false
			h.mu.Unlock()
			return dto.OperationReply{Problem: &dto.Problem{Code: "internal", Message: "Could not close the collection"}}
		}
	}
	h.mu.Lock()
	h.allowed = true
	h.mu.Unlock()
	h.quit()
	return dto.OperationReply{}
}
func (h *Host) CancelQuit(token string) dto.OperationReply {
	h.mu.Lock()
	cancelled := ""
	if token == h.token && !h.allowed && !h.completing {
		h.token = ""
		if h.pending != "" {
			cancelled = h.installProblem
			if cancelled == "" {
				cancelled = upgradeAbandoned
			}
			h.pending, h.installProblem = "", ""
		}
	}
	h.mu.Unlock()
	if cancelled != "" {
		h.emit("desktop:upgrade-cancelled", upgradeCancelled{Problem: dto.Problem{Code: "conflict", Message: cancelled}})
	}
	return dto.OperationReply{}
}
func (h *Host) ChooseCollection() (string, error) {
	if h.choose == nil {
		return "", nil
	}
	return h.choose()
}
func (h *Host) OpenExternal(raw string) dto.OperationReply {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return dto.OperationReply{Problem: &dto.Problem{Code: "invalid_argument", Message: "Only http and https links can be opened"}}
	}
	h.browse(raw)
	return dto.OperationReply{}
}
