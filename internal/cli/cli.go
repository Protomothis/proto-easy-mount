// Package cli wires providers, the store and the mounter into commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/store"
)

// Version is set at build time.
var Version = "dev"

type app struct {
	store   *store.Store
	account string
}

// Execute runs the command line.
func Execute() error {
	st, err := store.Default()
	if err != nil {
		return err
	}
	a := &app{store: st}

	root := &cobra.Command{
		Use:           store.AppName,
		Short:         "Sign in to cloud storage and mount it as a local drive",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&a.account, "account", "a", "", "account ID (see 'accounts')")
	root.AddCommand(a.loginCmd(), a.logoutCmd(), a.accountsCmd(), a.drivesCmd(), a.mountCmd(), a.tokenCmd(), a.providersCmd(), a.logsCmd())
	root.CompletionOptions.DisableDefaultCmd = true
	// rclone's packages register global flags we don't expose
	pflag.CommandLine.VisitAll(func(f *pflag.Flag) { f.Hidden = true })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return root.ExecuteContext(ctx)
}

// env is what providers get from the host program.
func (a *app) env() provider.Env {
	return core.Env(func(url string) error {
		fmt.Fprintln(os.Stderr, "브라우저에서 로그인하세요:", url)
		return browser.Open(url)
	})
}

func (a *app) load(ctx context.Context) (provider.Account, provider.Provider, provider.Secret, error) {
	acct, err := a.store.Account(a.account)
	if err != nil {
		return acct, nil, provider.Secret{}, err
	}
	p, err := provider.Get(acct.Provider)
	if err != nil {
		return acct, nil, provider.Secret{}, err
	}
	sec, err := a.store.FreshSecret(ctx, acct, p)
	if err != nil {
		return acct, p, sec, fmt.Errorf("%w (try 'login' again)", err)
	}
	return acct, p, sec, nil
}

func (a *app) loginCmd() *cobra.Command {
	var providerName string
	var values map[string]string
	cmd := &cobra.Command{
		Use:   "login <server>",
		Short: "Sign in through the browser and save the account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := provider.Get(providerName)
			if err != nil {
				return err
			}
			server, err := p.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("서버: %s (%s)\n", server.URL, server.Product)
			acct, sec, err := p.Login(cmd.Context(), a.env(), provider.LoginOptions{Server: server, Values: values})
			if err != nil {
				return err
			}
			acct, err = a.store.Save(acct, sec)
			if err != nil {
				return err
			}
			fmt.Printf("로그인됨: %s (%s)\n계정 ID: %s\n", acct.Display, acct.User, acct.ID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&providerName, "provider", "p", "opencloud", "provider: "+strings.Join(provider.Names(), ", "))
	cmd.Flags().StringToStringVar(&values, "set", nil, "provider option, e.g. --set client_id=abc (see 'providers')")
	return cmd
}

func (a *app) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke the session and forget the account",
		RunE: func(cmd *cobra.Command, args []string) error {
			acct, err := a.store.Account(a.account)
			if err != nil {
				return err
			}
			if p, err := provider.Get(acct.Provider); err == nil {
				if sec, err := a.store.Secret(acct.ID); err == nil {
					if err := p.Logout(cmd.Context(), acct, sec); err != nil {
						fmt.Fprintln(os.Stderr, "경고: 서버 세션 해제 실패:", err)
					}
				}
			}
			if err := a.store.Remove(acct.ID); err != nil {
				return err
			}
			fmt.Println("로그아웃됨:", acct.ID)
			return nil
		},
	}
}

func (a *app) accountsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accounts",
		Short: "List saved accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			accts, err := a.store.Accounts()
			if err != nil {
				return err
			}
			if len(accts) == 0 {
				fmt.Println("저장된 계정이 없습니다. 'login <server>'로 시작하세요.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tPROVIDER\tSERVER\tUSER")
			for _, x := range accts {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", x.ID, x.Provider, x.Server, x.User)
			}
			return w.Flush()
		},
	}
}

func (a *app) drivesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drives",
		Short: "List drives the account can mount",
		RunE: func(cmd *cobra.Command, args []string) error {
			acct, p, sec, err := a.load(cmd.Context())
			if err != nil {
				return err
			}
			drives, err := p.Drives(cmd.Context(), acct, sec)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKIND\tID")
			for _, d := range drives {
				fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, d.Kind, d.ID)
			}
			return w.Flush()
		},
	}
}

func (a *app) mountCmd() *cobra.Command {
	var all bool
	var target string
	cmd := &cobra.Command{
		Use:   "mount [drive name or ID...]",
		Short: "Mount drives until interrupted (default: personal space)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			acct, p, sec, err := a.load(ctx)
			if err != nil {
				return err
			}
			drives, err := p.Drives(ctx, acct, sec)
			if err != nil {
				return err
			}
			chosen, err := pickDrives(drives, args, all)
			if err != nil {
				return err
			}
			if target != "" && len(chosen) > 1 {
				return errors.New("--at works with a single drive")
			}
			if err := mount.SetCacheDir(a.store.CacheDir); err != nil {
				return err
			}

			var mounted []*mount.Mounted
			for _, d := range chosen {
				r, err := p.Remote(a.env(), acct, sec, d)
				if err != nil {
					return err
				}
				m, err := mount.Start(ctx, mount.Request{
					Name:   acct.ID + "-" + d.ID,
					Remote: r,
					Target: target,
					Label:  core.Label(p, d),
				})
				if err != nil {
					return fmt.Errorf("%s: %w", d.Name, err)
				}
				fmt.Printf("마운트됨: %s → %s\n", d.Name, m.Target)
				mounted = append(mounted, m)
			}
			fmt.Println("Ctrl+C로 해제합니다.")

			var wg sync.WaitGroup
			for _, m := range mounted {
				wg.Add(1)
				go func() { defer wg.Done(); m.Wait() }()
			}
			go func() {
				<-ctx.Done()
				for _, m := range mounted {
					m.Unmount()
				}
			}()
			wg.Wait()
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "mount every drive")
	cmd.Flags().StringVar(&target, "at", "", `drive letter ("Z:") or directory; default picks one`)
	return cmd
}

// pickDrives resolves names or IDs; with none given it picks the personal drive.
func pickDrives(drives []provider.Drive, want []string, all bool) ([]provider.Drive, error) {
	if all {
		return drives, nil
	}
	if len(want) == 0 {
		for _, d := range drives {
			if d.Kind == provider.KindPersonal {
				return []provider.Drive{d}, nil
			}
		}
		return nil, errors.New("no personal drive; name one (see 'drives')")
	}
	var out []provider.Drive
	for _, w := range want {
		found := false
		for _, d := range drives {
			if d.ID == w || strings.EqualFold(d.Name, w) {
				out = append(out, d)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no drive %q (see 'drives')", w)
		}
	}
	return out, nil
}

func (a *app) tokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "token",
		Short:  "Print a fresh access token (used by the mounter)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tok, err := core.Token(cmd.Context(), a.store, a.account)
			if err != nil {
				return err
			}
			fmt.Println(tok)
			return nil
		},
	}
}

func (a *app) logsCmd() *cobra.Command {
	var lines int
	var pathOnly bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the end of the desktop app's log",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := store.LogPath(a.store)
			if pathOnly {
				fmt.Println(path)
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("로그가 아직 없어요 (%s): %w", path, err)
			}
			all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			fmt.Fprintln(os.Stderr, "#", path)
			for _, l := range all[max(0, len(all)-lines):] {
				fmt.Println(l)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "number of lines from the end")
	cmd.Flags().BoolVar(&pathOnly, "path", false, "only print where the log is")
	return cmd
}

func (a *app) providersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List supported services",
		Run: func(cmd *cobra.Command, args []string) {
			for _, n := range provider.Names() {
				p, _ := provider.Get(n)
				fmt.Printf("%s\t%s — %s\n", n, p.Title(), p.Description())
				for _, o := range p.Options() {
					fmt.Printf("    --set %s=…\t%s (%s)\n", o.Key, o.Label, o.Hint)
				}
			}
		},
	}
}
