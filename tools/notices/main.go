// Command notices writes THIRD-PARTY-NOTICES: every Go module compiled into
// the release binaries with its license, plus the full license texts, which
// MIT and BSD licenses require to travel with the binaries.
//
//	go run ./tools/notices -o THIRD-PARTY-NOTICES.md
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The binaries whose dependencies are listed.
var packages = []string{"./cmd/proto-easy-mount-app", "./cmd/proto-easy-mount"}

const self = "github.com/protomothis/proto-easy-mount"

type module struct {
	path, version, dir string
	license            string // SPDX-like name
	files              []string
}

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|copyright|unlicense|notice)([-._].*)?$`)

func main() {
	out := flag.String("o", "THIRD-PARTY-NOTICES.md", "output file")
	flag.Parse()
	mods, err := listModules()
	if err != nil {
		log.Fatal(err)
	}
	var b bytes.Buffer
	if err := write(&b, mods); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: %d modules\n", *out, len(mods))
}

func listModules() ([]module, error) {
	args := append([]string{"list", "-tags", "cmount", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}"}, packages...)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]bool{}
	var mods []module
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) != 3 || f[0] == self || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		m := module{path: f[0], version: f[1], dir: f[2]}
		entries, err := os.ReadDir(m.dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && licenseFile.MatchString(e.Name()) {
				m.files = append(m.files, e.Name())
			}
		}
		if len(m.files) == 0 {
			return nil, fmt.Errorf("%s: no license file in %s", m.path, m.dir)
		}
		var names []string // several files: dual licensed (LICENSE-MIT, LICENSE-APACHE)
		for _, f := range m.files {
			text, _ := os.ReadFile(filepath.Join(m.dir, f))
			if n := classify(string(text)); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		m.license = strings.Join(names, " OR ")
		if m.license == "" {
			m.license = "기타 (전문 참고)"
		}
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].path < mods[j].path })
	return mods, nil
}

// classify names a license from its text; good enough for a summary, the
// full text below is what counts.
func classify(t string) string {
	head := t[:min(len(t), 4000)]
	switch {
	case strings.Contains(head, "Unlicense") && strings.Contains(head, "MIT"):
		return "Unlicense OR MIT"
	case strings.Contains(head, "Apache License"):
		return "Apache-2.0"
	case strings.Contains(head, "Mozilla Public License"):
		return "MPL-2.0"
	case strings.Contains(head, "CC0 1.0"):
		return "CC0-1.0"
	case strings.Contains(head, "Permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(head, "Redistribution and use in source and binary forms"):
		if strings.Contains(head, "Neither the name") || strings.Contains(head, "names of its") {
			return "BSD-3-Clause"
		}
		return "BSD-2-Clause"
	case strings.Contains(head, "Permission to use, copy, modify, and/or distribute"):
		return "ISC"
	case strings.Contains(head, "This is free and unencumbered"):
		return "Unlicense"
	}
	return "" // a notice or copyright file, not a license
}

func write(b *bytes.Buffer, mods []module) error {
	b.WriteString(`# 서드파티 고지

Proto Easy Mount는 GPL-3.0 라이선스로 배포되며, 아래 오픈소스를 포함하거나 함께 설치합니다.
이 파일은 ` + "`go run ./tools/notices`" + `로 만들어집니다.

## 함께 설치되는 프로그램

- **WinFsp** — Windows에서 드라이브를 연결하는 파일 시스템 드라이버. 없을 때만 공식 설치 파일을 그대로 설치합니다.
  Copyright (C) Bill Zissimopoulos. GPLv3 (FLOSS 예외 포함). 소스: https://github.com/winfsp/winfsp

## 고쳐서 포함한 코드

- **rclone** ` + "`cmd/cmount`" + ` — ` + "`internal/winfs`" + `는 rclone의 Windows 마운트 계층을 가져와 쓰기 경로를 고친 것입니다.
  Copyright (C) Nick Craig-Wood and rclone contributors. MIT. https://github.com/rclone/rclone
- **smartthings-pc-control** ` + "`internal/release`" + ` · ` + "`tools/signmanifest`" + ` — 같은 저자(Protomothis)의 MIT 코드에서 가져온 업데이트 확인·서명 코드.

## Go 모듈

| 모듈 | 버전 | 라이선스 |
|---|---|---|
`)
	for _, m := range mods {
		fmt.Fprintf(b, "| [%s](https://%s) | %s | %s |\n", m.path, m.path, m.version, m.license)
	}
	b.WriteString("\n## 라이선스 전문\n")
	for _, m := range mods {
		for _, f := range m.files {
			text, err := os.ReadFile(filepath.Join(m.dir, f))
			if err != nil {
				return err
			}
			s := strings.ReplaceAll(strings.TrimSpace(string(text)), "\r\n", "\n")
			fmt.Fprintf(b, "\n### %s — %s\n\n````text\n%s\n````\n", m.path, f, s)
		}
	}
	return nil
}
