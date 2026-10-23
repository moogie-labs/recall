package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataPTYImportDoesNotRequireEditor(t *testing.T) {
	isolated(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("developer packaging/PTY checks require Python3")
	}
	cmd := exec.Command(python, "-c", `import runpy,shutil,sys
assert shutil.which('nvim') is None
module=runpy.run_path(sys.argv[1],run_name='recall_pty')
assert callable(module['Session'])
print('PASS metadata PTY import without an editor')`, filepath.Join(repository, "scripts", "verify-neovim.py"))
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir()) // no editor or tools to discover
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("metadata-only PTY import: %v %s", err, out)
	}
}

func TestIntegrationExecutableRejectsInferenceDispatch(t *testing.T) {
	isolated(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	cmd := exec.CommandContext(ctx, exe, "_embedding-worker", "--root", root)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("test executable hosted inference instead of rejecting it: %v %s", err, out)
	}
	for _, path := range []string{filepath.Join(root, "config.json"), filepath.Join(os.Getenv("HOME"), ".cache", "recall")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected worker entry touched runtime data: %s: %v", path, err)
		}
	}
}
