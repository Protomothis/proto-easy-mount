// Command mkwinres writes Windows resource objects (.syso) for the binaries:
// the app icon drawn by internal/icon, version info and a manifest.
// The Go linker picks the .syso up automatically when building for Windows.
//
//	go run ./tools/mkwinres -version 0.1.0
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"

	"github.com/protomothis/proto-easy-mount/internal/icon"
)

type target struct {
	dir         string
	file        string
	description string
}

var targets = []target{
	{"cmd/proto-easy-mount-app", "proto-easy-mount-app.exe", "Proto Easy Mount"},
	{"cmd/proto-easy-mount", "proto-easy-mount.exe", "Proto Easy Mount (명령줄)"},
}

func main() {
	ver := flag.String("version", "0.0.0-dev", "semantic version")
	assets := flag.String("assets", "", "also write installer images (app.ico, wizard-small.bmp) to this directory")
	flag.Parse()
	if *assets != "" {
		if err := writeAssets(*assets); err != nil {
			log.Fatal(err)
		}
	}
	nums, err := parseVersion(*ver)
	if err != nil {
		log.Fatal(err)
	}
	ico, err := winres.LoadICO(bytes.NewReader(icon.ICO(false)))
	if err != nil {
		log.Fatal(err)
	}

	for _, t := range targets {
		rs := &winres.ResourceSet{}
		if err := rs.SetIcon(winres.Name("APPICON"), ico); err != nil {
			log.Fatal(err)
		}
		vi := version.Info{FileVersion: nums, ProductVersion: nums}
		for k, v := range map[string]string{
			version.ProductName:      "Proto Easy Mount",
			version.ProductVersion:   *ver,
			version.FileVersion:      *ver,
			version.FileDescription:  t.description,
			version.CompanyName:      "Protomothis",
			version.LegalCopyright:   "Protomothis",
			version.OriginalFilename: t.file,
			version.InternalName:     t.file,
		} {
			vi.Set(0, k, v)
		}
		rs.SetVersionInfo(vi)
		rs.SetManifest(winres.AppManifest{
			Identity:            winres.AssemblyIdentity{Name: "Protomothis.ProtoEasyMount", Version: nums},
			Description:         t.description,
			ExecutionLevel:      winres.AsInvoker,
			DPIAwareness:        winres.DPIPerMonitorV2,
			UseCommonControlsV6: true,
		})

		for _, arch := range []winres.Arch{winres.ArchAMD64, winres.ArchARM64} {
			path := filepath.Join(t.dir, fmt.Sprintf("rsrc_windows_%s.syso", arch))
			if err := write(rs, path, arch); err != nil {
				log.Fatal(err)
			}
		}
	}
}

func write(rs *winres.ResourceSet, path string, arch winres.Arch) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := rs.WriteObject(f, arch); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

var semver = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// parseVersion maps "1.2.3-rc.1" to the four numbers Windows wants.
func parseVersion(s string) ([4]uint16, error) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return [4]uint16{}, fmt.Errorf("version %q is not semantic (x.y.z)", s)
	}
	var out [4]uint16
	for i := 0; i < 3; i++ {
		n, _ := strconv.ParseUint(m[i+1], 10, 16)
		out[i] = uint16(n)
	}
	return out, nil
}
