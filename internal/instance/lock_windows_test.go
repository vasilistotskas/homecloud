//go:build windows

package instance

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAcquireIsExclusive(t *testing.T) {
	name := fmt.Sprintf("homecloud-test-%d", time.Now().UnixNano())
	l, err := Acquire(name)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, secondErr := Acquire(name); !errors.Is(secondErr, ErrAlreadyRunning) {
		t.Fatalf("second Acquire: want ErrAlreadyRunning, got %v", secondErr)
	}
	if releaseErr := l.Release(); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	l2, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatal(err)
	}
}
