// Package instance keeps the desktop app to one process per user. A second
// launch asks the running one to show its window and then exits.
package instance

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// Primary is held by the first process.
type Primary struct {
	lock *flock.Flock
	ln   net.Listener
}

// Acquire returns a Primary when this is the first process. Otherwise it
// signals the running process to activate and returns nil.
func Acquire(dir string, onActivate func()) (*Primary, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "app.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	portFile := filepath.Join(dir, "app.port")
	if !ok {
		return nil, activate(portFile)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(portFile, []byte(fmt.Sprint(port)), 0o600); err != nil {
		ln.Close()
		lock.Unlock()
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Close()
			if strings.TrimSpace(line) == "show" {
				onActivate()
			}
		}
	}()
	return &Primary{lock: lock, ln: ln}, nil
}

func activate(portFile string) error {
	b, err := os.ReadFile(portFile)
	if err != nil {
		return fmt.Errorf("already running, but cannot reach it: %w", err)
	}
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strings.TrimSpace(string(b)), 2*time.Second)
	if err != nil {
		return fmt.Errorf("already running, but cannot reach it: %w", err)
	}
	defer c.Close()
	_, err = c.Write([]byte("show\n"))
	return err
}

// Release frees the lock.
func (p *Primary) Release() {
	p.ln.Close()
	p.lock.Unlock()
}
