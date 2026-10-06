package platform

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockFileExcludesAndWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	first, err := LockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var acquired atomic.Bool
	done := make(chan error, 1)
	go func() {
		second, err := LockFile(path)
		if err == nil {
			acquired.Store(true)
			err = second.Unlock()
		}
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("a second locker got the lock while the first held it")
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || !acquired.Load() {
			t.Fatalf("second locker: acquired=%v, %v", acquired.Load(), err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second locker never got the lock after the first released it")
	}
}
