package instance

import (
	"testing"
	"time"
)

func TestSecondLaunchActivatesFirst(t *testing.T) {
	dir := t.TempDir()
	shown := make(chan struct{}, 1)
	p, err := Acquire(dir, func() { shown <- struct{}{} })
	if err != nil || p == nil {
		t.Fatalf("first acquire: %v, %v", p, err)
	}
	defer p.Release()

	second, err := Acquire(dir, func() { t.Error("second instance activated") })
	if err != nil || second != nil {
		t.Fatalf("second acquire: %v, %v", second, err)
	}
	select {
	case <-shown:
	case <-time.After(3 * time.Second):
		t.Fatal("first instance was not activated")
	}
}
