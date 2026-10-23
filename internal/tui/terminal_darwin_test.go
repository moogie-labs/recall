package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A PTY is owned by this child test, never the user's terminal. No Neovim or
// model is needed for the regression: an interrupted editor leaves raw mode.
func TestCancelledEditorRestoresTerminalBeforeTUIRecaptures(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PTY regression harness requires developer Python3")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", `import os,pty,subprocess,sys,termios
master,slave=pty.openpty()
try:
 p=subprocess.run([sys.argv[1],'-test.run=^TestEditorTerminalFixture$'],stdin=slave,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=dict(os.environ,RECALL_TUI_TERMINAL_FIXTURE='1'),timeout=15)
 assert p.returncode==0,(p.stdout,p.stderr)
finally:
 os.close(master);os.close(slave)
`, exe)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "RECALL_HOME="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PTY regression: %v %s", err, out)
	}
}

func TestEditorTerminalFixture(t *testing.T) {
	if os.Getenv("RECALL_TUI_TERMINAL_FIXTURE") != "1" {
		t.Skip("private PTY helper")
	}
	m, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.ctx = ctx
	started := filepath.Join(t.TempDir(), "started")
	before, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `stty raw -echo; : > "$1"; exec sleep 60`, "recall-owned-editor", started)
	cmd.Stdin = os.Stdin
	p := m.editorProcess(cmd)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	if err := p.Run(); err == nil {
		t.Fatal("cancelled editor succeeded")
	}
	<-done
	after, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	// Darwin toggles transient PENDIN bookkeeping on a raw->canonical restore.
	before.Lflag &^= unix.PENDIN
	after.Lflag &^= unix.PENDIN
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("cancelled editor left terminal state changed: before=%+v after=%+v", before, after)
	}
}
