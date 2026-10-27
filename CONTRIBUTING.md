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

## 이슈 · 마일스톤 · 브랜치

작업은 이슈에서 시작하고 버전(마일스톤)별로 모읍니다.

```
main ──► develop ──► milestone/vX.Y.Z      작업할 때 (아래로 따서 커밋 · 푸시)
main ◄── develop ◄── milestone/vX.Y.Z      릴리스할 때 (위로 합치고 main에 태그)
```

- **이슈** — 기능 · 버그 하나마다 이슈를 만들고 마일스톤 `vX.Y.Z`에 넣습니다. 라벨은 `enhancement` · `bug` · `documentation`.
- **`main`** — 릴리스된 코드만 있습니다. 정식 태그 `vX.Y.Z`는 여기에만 답니다. 직접 커밋하지 않습니다.
- **`develop`** — `main`에서 딴 통합 브랜치. 마일스톤 브랜치는 여기서 땁니다.
- **`milestone/vX.Y.Z`** — 그 버전의 작업을 커밋하고 푸시하는 곳. 큰 작업은 여기서 짧은 브랜치(`feat/…`, `fix/…`, `docs/…`)를 따서 합칩니다.
- 커밋 본문에 `Closes #n`을 적습니다. 이슈는 `main`에 합쳐질 때(릴리스 때) 닫힙니다.
- CI는 `main` · `develop` · `milestone/**` 푸시와 `main`으로 가는 PR에서 돕니다.

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

1. `milestone/vX.Y.Z`에서 `CHANGELOG.md`의 `[Unreleased]`를 `## [vX.Y.Z] - YYYY-MM-DD`로 바꾸고 커밋합니다.
2. 테스트 버전이 필요하면 마일스톤 브랜치에 `vX.Y.Z-rc.N` 태그를 답니다. 시험판으로 올라가 [테스트 버전도 받기]를 켠 앱만 받습니다.
3. 직접 설치해 확인한 뒤 역방향으로 합칩니다: `milestone/vX.Y.Z` → `develop` → `main` (`git merge --no-ff`).
4. `main`에 정식 태그 `vX.Y.Z`를 달고 푸시합니다. Actions가 설치 파일을 만들어 Latest로 올립니다. 마일스톤을 닫습니다.

릴리스에는 설치 파일, 명령줄 `proto-easy-mount.exe`, `update.json`(설치 파일의 SHA-256)과 `update.json.sig`가 붙습니다.
앱은 서명이 `internal/release`에 들어 있는 공개 키로 확인되고 해시가 맞는 설치 파일만 설치합니다.
서명 키는 저장소 시크릿 `UPDATE_SIGNING_KEY`에만 있습니다. 키를 바꾸려면 `go run ./tools/signmanifest genkey`로 새로 만들고
공개 키를 `internal/release/manifest.go`에 넣은 버전을 먼저 내야 합니다(옛 앱은 그 뒤 버전을 자동으로 받지 못합니다).

## 서드파티 고지

의존성을 바꿨으면 `go run ./tools/notices`로 `THIRD-PARTY-NOTICES.md`를 다시 만듭니다. 설치 파일에는 빌드 때 새로 만든 것이 들어갑니다.
