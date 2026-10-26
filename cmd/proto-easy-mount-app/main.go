// Command proto-easy-mount-app is the desktop app: a tray icon plus a window.
// It also answers "token --account <id>" without opening any UI, because
// mounts started by this process re-run its own executable to refresh tokens.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"

	"fyne.io/systray"

	"github.com/protomothis/proto-easy-mount/internal/autostart"
	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/gui"
	"github.com/protomothis/proto-easy-mount/internal/instance"
	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/store"
	"github.com/protomothis/proto-easy-mount/internal/tray"
	"github.com/protomothis/proto-easy-mount/internal/update"
)

// Version is set at build time.
var Version = "dev"

func main() {
	st, err := store.Default()
	if err != nil {
		fatal(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "token" {
		printToken(st, os.Args[2:])
		return
	}
	background := slices.Contains(os.Args[1:], autostart.BackgroundFlag)

	var ui atomic.Pointer[gui.UI]
	primary, err := instance.Acquire(st.CacheDir, func() {
		if u := ui.Load(); u != nil {
			u.Show()
		}
	})
	if err != nil {
		fatal(err)
	}
	if primary == nil {
		return // the running instance was asked to show itself
	}
	defer primary.Release()

	mount.LogToFile(store.LogPath(st))
	slog.Info(fmt.Sprintf("Proto Easy Mount %s 시작 (%s/%s, rclone %s, background=%v)",
		Version, runtime.GOOS, runtime.GOARCH, mount.RcloneVersion(), background))
	svc, err := core.New(st, core.Env(browser.Open))
	if err != nil {
		fatal(err)
	}
	u := gui.New(svc, Version, systray.Quit)
	u.LogPath = store.LogPath(st)
	ui.Store(u)

	t := tray.New(svc, u.Show, systray.Quit)
	t.Version, t.NewVersion = "v"+strings.TrimPrefix(Version, "v"), u.NewVersion
	u.OnUpdate = t.Refresh
	// An update quits the app first, so its drives unmount cleanly before
	// the installer replaces the files; the installer starts it again.
	var installer atomic.Pointer[string]
	u.Install = func(path string) {
		installer.Store(&path)
		systray.Quit()
	}

	go svc.RefreshAll(context.Background())
	go svc.Watch(context.Background())
	go u.WatchUpdates(context.Background())
	if !background || len(svc.Snapshot()) == 0 {
		u.Show()
	}

	run(t.OnReady, func() {
		t.Close()
		svc.Shutdown()
		if p := installer.Load(); p != nil {
			if err := update.Install(*p); err != nil {
				slog.Error(fmt.Sprintf("업데이트 설치 시작 실패: %v", err))
			}
		}
	})
}

func printToken(st *store.Store, args []string) {
	var account string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--account" {
			account = args[i+1]
		}
	}
	tok, err := core.Token(context.Background(), st, account)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(tok)
}
