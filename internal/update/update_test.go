package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/protomothis/proto-easy-mount/internal/release"
)

// fakeGitHub serves a release list whose newest release carries an
// installer and a manifest signed with a throwaway key.
func fakeGitHub(t *testing.T, installer []byte, tamper bool) (api string, restore func()) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := release.PublicKey
	release.PublicKey = pub

	sum := sha256.Sum256(installer)
	name := release.InstallerName("v0.2.0")
	man, _ := release.EncodeManifest(&release.Manifest{Version: "v0.2.0",
		Assets: []release.ManifestAsset{{Name: name, SHA256: hex.EncodeToString(sum[:]), Arch: "amd64", Size: int64(len(installer))}}})
	sig := release.EncodeSignature(release.Sign(man, priv))

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			asset := func(n string) map[string]any {
				return map[string]any{"name": n, "browser_download_url": srv.URL + "/dl/" + n}
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v0.3.0-rc.1", "prerelease": true, "body": "## [v0.3.0-rc.1]\n\n- 시험"},
				{"tag_name": "v0.2.0", "html_url": "https://x/v0.2.0", "body": "## [v0.2.0] - 2026-10-27\n\n- 새 기능",
					"assets": []any{asset(name), asset(release.ManifestName), asset(release.ManifestSigName)}},
				{"tag_name": "v0.1.0", "body": "- 처음"},
			})
		case "/dl/" + name:
			b := installer
			if tamper {
				b = append([]byte("x"), installer[1:]...)
			}
			w.Write(b)
		case "/dl/" + release.ManifestName:
			w.Write(man)
		case "/dl/" + release.ManifestSigName:
			w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv.URL + "/releases", func() { srv.Close(); release.PublicKey = old }
}

func TestCheckAndDownload(t *testing.T) {
	installer := []byte("MZ fake installer")
	api, restore := fakeGitHub(t, installer, false)
	defer restore()

	a, err := check(context.Background(), api, "0.1.0", false)
	if err != nil || a == nil {
		t.Fatalf("check: %v %v", a, err)
	}
	if a.Version != "v0.2.0" || !strings.Contains(a.Notes, "## v0.2.0\n\n- 새 기능") || strings.Contains(a.Notes, "시험") {
		t.Fatalf("available = %+v", a)
	}
	path, err := a.Download(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(installer) || filepath.Base(path) != "ProtoEasyMount-Setup-0.2.0.exe" {
		t.Fatalf("downloaded %s: %q", path, got)
	}
}

func TestCheckChannelsAndCurrent(t *testing.T) {
	api, restore := fakeGitHub(t, []byte("MZ"), false)
	defer restore()
	ctx := context.Background()
	if a, _ := check(ctx, api, "0.2.0", false); a != nil {
		t.Fatalf("offered %s to the newest version", a.Version)
	}
	if a, _ := check(ctx, api, "0.2.0", true); a == nil || a.Version != "v0.3.0-rc.1" {
		t.Fatalf("test channel offered %+v", a)
	}
	if a, _ := check(ctx, api, "0.1.0-dev", true); a != nil {
		t.Fatal("a development build was offered an update")
	}
}

// An installer that doesn't match the signed hash is refused and removed.
func TestDownloadRejectsTamperedInstaller(t *testing.T) {
	api, restore := fakeGitHub(t, []byte("MZ fake installer"), true)
	defer restore()
	a, err := check(context.Background(), api, "0.1.0", false)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := a.Download(context.Background(), dir); err == nil {
		t.Fatal("tampered installer accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("tampered file kept: %v", left)
	}
}
