package instance

import (
	"testing"
	"time"
)

func TestSecondLaunchActivatesFirst(t *testing.T) {
	dir := t.TempDir()
	got := make(chan string, 2)
	p, err := Acquire(dir, "", func(link string) { got <- link })
	if err != nil || p == nil {
		t.Fatalf("first acquire: %v, %v", p, err)
	}
	defer p.Release()

	for _, link := range []string{"", "proto-easy-mount://add?provider=opencloud&server=https%3A%2F%2Fa.example"} {
		second, err := Acquire(dir, link, func(string) { t.Error("second instance activated") })
		if err != nil || second != nil {
			t.Fatalf("second acquire: %v, %v", second, err)
		}
		select {
		case l := <-got:
			if l != link {
				t.Fatalf("first instance got %q, want %q", l, link)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("first instance was not activated")
		}
	}
}
