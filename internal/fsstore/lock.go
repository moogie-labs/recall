package fsstore

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"recall/internal/model"
)

// WithLock holds the cooperating, cross-process source lock for fn. It is not
// reentrant: application methods must call a private syncLocked helper instead
// of recursively acquiring this lock via public Sync. Cancellation interrupts
// lock acquisition; fn is responsible for observing its own operation context.
func (s *Store) WithLock(ctx context.Context, fn func() error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A distinct descriptor per acquisition is essential: flock on one shared
	// descriptor would not serialize concurrent goroutines in the same Store.
	// Exclusive creation also avoids racing nonexclusive initial creators.
	f, err := openNoFollow(s.run, "store.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if model.ErrorCode(err) == "already_exists" {
		f, err = openNoFollow(s.run, "store.lock", os.O_RDWR, 0)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return appError("invalid_argument", "run/store.lock", "a regular file is required")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer func() { err = errors.Join(err, unix.Flock(int(f.Fd()), unix.LOCK_UN)) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
