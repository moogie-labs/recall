package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	dto "recall/internal/desktop"
	"sync"
)

type quitRequest struct {
	Token string `json:"token"`
}
type Host struct {
	mu             sync.Mutex
	ready, allowed bool
	token          string
	emit           func(string, any)
	quit           func()
	browse         func(string)
}

func newHost(emit func(string, any), quit func(), browse func(string)) *Host {
	return &Host{emit: emit, quit: quit, browse: browse}
}
func (h *Host) Ready() { h.mu.Lock(); h.ready = true; h.mu.Unlock() }
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
	h.allowed = true
	h.mu.Unlock()
	h.quit()
	return dto.OperationReply{}
}
func (h *Host) OpenExternal(raw string) dto.OperationReply {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return dto.OperationReply{Problem: &dto.Problem{Code: "invalid_argument", Message: "Only http and https links can be opened"}}
	}
	h.browse(raw)
	return dto.OperationReply{}
}
