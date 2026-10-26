package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/bmp"

	"github.com/protomothis/proto-easy-mount/internal/icon"
)

// wizardSizes are Inno Setup's small wizard image sizes for 100%–250% scaling.
var wizardSizes = []int{55, 64, 73, 92, 110, 138}

// writeAssets renders the icon for the installer: an .ico for the setup
// executable and opaque BMPs (Inno Setup has no alpha) for the wizard header.
func writeAssets(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "app.ico"), icon.ICO(false), 0o644); err != nil {
		return err
	}
	var names []string
	for _, s := range wizardSizes {
		name := fmt.Sprintf("wizard-small-%d.bmp", s)
		if err := writeBMP(filepath.Join(dir, name), s); err != nil {
			return err
		}
		names = append(names, name)
	}
	// list for the .iss WizardSmallImageFile directive
	return os.WriteFile(filepath.Join(dir, "wizard-small.txt"), []byte(strings.Join(names, ",")), 0o644)
}

func writeBMP(path string, size int) error {
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), icon.Image(size, false), image.Point{}, draw.Over)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := bmp.Encode(f, canvas); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
