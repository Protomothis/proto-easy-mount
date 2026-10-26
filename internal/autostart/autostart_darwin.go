package autostart

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"
)

const label = "com.protomothis.proto-easy-mount"

var plist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array><string>{{.Exe}}</string><string>{{.Flag}}</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`))

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Enabled reports whether a launch agent points at this executable.
func Enabled() bool {
	p, err := agentPath()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	exe, eerr := command()
	return err == nil && eerr == nil && bytes.Contains(b, []byte(exe))
}

// Set turns starting at login on or off.
func Set(on bool) error {
	p, err := agentPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := command()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := plist.Execute(&buf, map[string]string{"Label": label, "Exe": exe, "Flag": BackgroundFlag}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o644)
}
