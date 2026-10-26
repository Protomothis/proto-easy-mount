// Package update finds a newer release on GitHub, downloads its installer
// once the release's signed manifest vouches for it, and starts it.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/release"
)

// maxInstaller caps the download; the installer is around 12 MB.
const maxInstaller = 200 << 20

// Client is used for every request; GitHub serves assets via redirects.
var Client = &http.Client{Timeout: 5 * time.Minute}

// Available is a release newer than the running version.
type Available struct {
	Version string // tag, v1.2.3
	Page    string // release page, for reading or installing by hand
	// Notes are the notes of every release since the running one, newest
	// first, each under its version.
	Notes string

	current string
	rel     release.Info
}

// Check asks GitHub for a release newer than current: only stable releases,
// or test versions too when pre is set. It returns nil, nil when the running
// version is the newest, and for development builds.
func Check(ctx context.Context, current string, pre bool) (*Available, error) {
	return check(ctx, release.API, current, pre)
}

func check(ctx context.Context, api, current string, pre bool) (*Available, error) {
	if strings.HasSuffix(current, "-dev") {
		return nil, nil // a local build; it has no release to update from
	}
	rel, list, err := release.FetchFor(ctx, Client, api, pre)
	if errors.Is(err, release.ErrNoRelease) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !release.IsNewer(current, rel.TagName) {
		return nil, nil
	}
	a := &Available{Version: rel.TagName, Page: rel.HTMLURL, current: current, rel: *rel}
	if a.Page == "" {
		a.Page = release.Page
	}
	var notes []string
	for _, r := range release.NotesSinceFor(list, current, pre) {
		n := "## " + r.TagName
		if body := r.Notes(); body != "" {
			n += "\n\n" + body
		}
		notes = append(notes, n)
	}
	a.Notes = strings.Join(notes, "\n\n")
	return a, nil
}

// Download fetches the installer into dir and checks it against the signed
// manifest, returning its path. Nothing that fails a check is kept.
func (a *Available) Download(ctx context.Context, dir string) (string, error) {
	m, err := release.FetchManifest(ctx, Client, &a.rel)
	if err != nil {
		return "", err
	}
	if !m.Allows(a.current) {
		return "", fmt.Errorf("%s 는 %s 이상에서만 자동으로 업데이트할 수 있어요", m.Version, m.MinVersion)
	}
	asset, ok := m.AssetFor(runtime.GOARCH)
	if !ok || asset.Name != release.InstallerName(a.Version) {
		return "", fmt.Errorf("%s 에 이 PC용 설치 파일이 없어요", a.Version)
	}
	url := release.AssetURL(&a.rel, asset.Name)
	if url == "" {
		return "", fmt.Errorf("%s 를 릴리스에서 찾지 못했어요", asset.Name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, asset.Name)
	if err := fetch(ctx, url, path, asset); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func fetch(ctx context.Context, url, path string, asset *release.ManifestAsset) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("설치 파일 받기: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxInstaller+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxInstaller {
		return errors.New("설치 파일이 너무 커요")
	}
	return asset.Verify(hex.EncodeToString(h.Sum(nil)), n)
}

// Install starts a downloaded installer quietly. It replaces the app, which
// must have exited (and unmounted its drives) by the time files are copied,
// and starts it again afterwards (/RELAUNCH, see installer/*.iss).
func Install(path string) error {
	cmd := exec.Command(path, "/SILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/RELAUNCH=1")
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
