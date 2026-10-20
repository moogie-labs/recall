package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These use the production backend and real filesystem changes, not injected
// events. Do not refresh after the parent change: that was the missing window
// in the original regression, in which fsnotify reinstalled implicit watches.
func TestWatchSourceIgnoresOutsideActivityAfterParentChange(t *testing.T) {
	testOutsideActivity(t, true)
}

func TestWatchSourceIgnoresNewSymlinksBetweenRefreshes(t *testing.T) {
	testOutsideActivity(t, false)
}

func testOutsideActivity(t *testing.T, existingLink bool) {
	t.Helper()
	for _, kind := range []string{"directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			outsideFile := filepath.Join(outside, "outside.task.md")
			write(t, outsideFile, "outside body")
			target := outside
			if kind == "file" {
				target = outsideFile
			}
			link := filepath.Join(root, "link")
			makeLink := func() {
				t.Helper()
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			}
			if existingLink {
				makeLink()
			}
			w, err := newWatchSource()
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if err := w.refresh(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			if !existingLink {
				makeLink()
			}
			// The lexically later entry ensures fsnotify has processed "link"
			// when its CREATE is delivered. A polling fallback may coalesce this
			// into a root-directory hint instead; either is a responsive save.
			inside := filepath.Join(root, "zzz.note.md")
			write(t, inside, "inside body")
			timeout := time.NewTimer(2 * time.Second)
			defer timeout.Stop()
		waiting:
			for {
				select {
				case event := <-w.events:
					if event.Name == inside || event.Name == root {
						break waiting
					}
				case err := <-w.errors:
					t.Fatalf("watcher error: %v", err)
				case <-timeout.C:
					t.Fatal("ordinary inside save was not observed")
				}
			}
			// Let already queued inside events settle, without invoking refresh.
			quiet := time.NewTimer(200 * time.Millisecond)
			defer quiet.Stop()
		settling:
			for {
				select {
				case <-w.events:
					quiet.Reset(200 * time.Millisecond)
				case err := <-w.errors:
					t.Fatalf("watcher error: %v", err)
				case <-quiet.C:
					break settling
				case <-timeout.C:
					t.Fatal("inside events did not settle")
				}
			}
			if kind == "directory" {
				write(t, filepath.Join(outside, "new.note.md"), "new outside body")
			} else {
				write(t, outsideFile, "outside body changed")
			}
			select {
			case event := <-w.events:
				t.Fatalf("outside activity generated a reconciliation hint after parent change: %v", event)
			case err := <-w.errors:
				t.Fatalf("outside activity generated an error: %v", err)
			case <-time.After(400 * time.Millisecond):
			}
		})
	}
}

func BenchmarkDarwinMetadataSnapshot1000Files(b *testing.B) {
	root := b.TempDir()
	for folder := range 10 {
		dir := filepath.Join(root, fmt.Sprintf("folder-%d", folder))
		if err := os.Mkdir(dir, 0700); err != nil {
			b.Fatal(err)
		}
		for file := range 100 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%d.note.md", file)), []byte("body"), 0600); err != nil {
				b.Fatal(err)
			}
		}
	}
	anchor, err := os.Open(root)
	if err != nil {
		b.Fatal(err)
	}
	defer anchor.Close()
	b.ReportAllocs()
	for b.Loop() {
		entries, err := metadataSnapshot(b.Context(), anchor, root)
		if err != nil || len(entries) != 1011 {
			b.Fatalf("snapshot has %d entries: %v", len(entries), err)
		}
	}
}
