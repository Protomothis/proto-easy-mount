# 기여 안내

## 버전

[유의적 버전](https://semver.org/lang/ko/)을 따릅니다. 태그가 곧 버전입니다.

| 태그 | 뜻 | 받는 사람 |
|---|---|---|
| `v0.2.0` | 정식 릴리스. GitHub의 Latest | 모든 앱(자동 업데이트) |
| `v0.2.0-rc.1` | 테스트 버전(시험판) | [테스트 버전도 받기]를 켠 앱만 |

- 정식이 아닌 버전은 `-rc.N`처럼 점으로 번호를 붙입니다. `rc.2 < rc.10 < 정식` 순서로 비교합니다(`internal/release`).
- 1.0 전까지는 기능이 늘면 MINOR, 고치기만 하면 PATCH를 올립니다.
- 로컬 빌드는 버전이 `-dev`로 끝나 업데이트 알림을 받지 않습니다.

## 브랜치

- `main`은 릴리스할 수 있는 상태를 유지합니다. 태그는 `main`에만 답니다.
- 큰 작업은 `milestone/vX.Y.Z`에 모으고, 작업마다 짧은 브랜치(`feat/…`, `fix/…`, `docs/…`)를 땁니다.

## 커밋

- 제목은 무엇을 하는지 한 줄로(50자 안팎) 씁니다. 본문에는 **왜** 바꾸는지, 실측 결과와 버린 대안을 둡니다.
- 사용자에게 보이는 변경은 `CHANGELOG.md`의 `[Unreleased]`에 한두 문장으로 적습니다.
- AI 도구의 도움을 받았으면 본문 끝에 `Co-Authored-By:` 트레일러를 붙입니다.

## 테스트

```bash
gofmt -l .                               # 비어 있어야 합니다
go vet -tags cmount ./...
go test -tags cmount ./...
PEM_SNAPSHOT_DIR=/tmp/shots go test -tags cmount -run TestLayout ./internal/gui/   # 화면을 PNG로
```

Windows 빌드는 `cmount` 태그가 있어야 마운트 기능이 들어갑니다(cgo 불필요).

## 릴리스

1. `CHANGELOG.md`의 `[Unreleased]`를 `## [vX.Y.Z] - YYYY-MM-DD`로 바꾸고 커밋합니다.
2. 테스트 버전부터: `git tag vX.Y.Z-rc.1 && git push origin vX.Y.Z-rc.1`. Actions가 설치 파일을 만들어 시험판으로 올립니다.
3. 직접 설치해 확인한 뒤 정식 태그 `vX.Y.Z`를 답니다. 시험판 노트는 `[Unreleased]`, 정식 노트는 그 버전 항목입니다.

릴리스에는 설치 파일, 명령줄 `proto-easy-mount.exe`, `update.json`(설치 파일의 SHA-256)과 `update.json.sig`가 붙습니다.
앱은 서명이 `internal/release`에 들어 있는 공개 키로 확인되고 해시가 맞는 설치 파일만 설치합니다.
서명 키는 저장소 시크릿 `UPDATE_SIGNING_KEY`에만 있습니다. 키를 바꾸려면 `go run ./tools/signmanifest genkey`로 새로 만들고
공개 키를 `internal/release/manifest.go`에 넣은 버전을 먼저 내야 합니다(옛 앱은 그 뒤 버전을 자동으로 받지 못합니다).

## 서드파티 고지

의존성을 바꿨으면 `go run ./tools/notices`로 `THIRD-PARTY-NOTICES.md`를 다시 만듭니다. 설치 파일에는 빌드 때 새로 만든 것이 들어갑니다.
