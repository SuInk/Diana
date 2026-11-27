<div align="center">

# Diana

[简体中文](./README.md) · [English](./README.en.md) · [日本語](./README.ja.md) · **한국어** · [Français](./README.fr.md) · [Deutsch](./README.de.md) · [Nederlands](./README.nl.md)

**사람처럼 대화하고, 에이전트처럼 일합니다.**

셀프 호스팅 단일 바이너리 AI 에이전트 — `@` 멘션 없이도 분위기를 파악해 자연스럽게 그룹 채팅에 참여하고, 장기 기억·이미지 인식·링크/파일 분석·노래 신청·웹 검색·Skills / MCP·로컬 코딩 에이전트까지 처리합니다. 모든 데이터와 판단 기록은 내 기기에만 남습니다.

<sub>OneBot v11 · Telegram · QQ 공식 봇 · WeChat · iMessage · 내장 콘솔 — 하나의 인스턴스로 동시 실행</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[공식 사이트](https://suink.github.io/Diana/ko/) · [문서(영문)](https://suink.github.io/Diana/en/deploy.html) · [라이브 데모](https://suink.github.io/Diana/demo/) · [최신 릴리스](https://github.com/SuInk/Diana/releases/latest)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana 콘솔 개요: 채널 상태, 메시지 통계, 시스템 리소스 및 실시간 이벤트 스트림" width="100%" />

## 왜 Diana인가요

셀프 호스팅 채팅 봇은 흔히 두 극단으로 나뉩니다. `@`로 부르지 않으면 묵묵부답이거나(혹은 능동 응답을 켜자마자 도배하는) 장난감이거나, Python/Node 환경과 YAML 설정을 한참 붙잡고도 왜 답장을 안 하는지 알 수 없는 블랙박스이거나.

Diana는 ‘자연스러운 대화 감각’과 ‘실제로 일을 해내는 에이전트 역량’을 하나의 Go 단일 바이너리에 담았습니다. 한쪽은 주요 LLM(OpenAI 호환 API, Gemini, Anthropic)에, 다른 한쪽은 자주 쓰는 메신저에 동시에 연결하고 내장 웹 콘솔에서 모든 것을 관리합니다.

- **눈치 있게 말하고, 조용할 땐 조용하게** — 멘션 없이도 적절한 순간에만 대화에 끼어듭니다. 계층형 메모리와 편집 가능한 노트북으로 약속과 규칙을 기억하며, 사진·영상 링크·노래 신청·이모티콘도 자연스럽게 다룹니다.
- **채팅 안에서 곧바로 업무 처리** — 웹 검색, 문서 분석, 다이어그램 이미지 렌더링이 기본 탑재되어 있습니다. Skills·MCP·브라우저 도구는 물론 로컬 Claude Code / Codex에 코딩 작업까지 맡길 수 있습니다.
- **3분 만에 실행, 모든 과정을 투명하게** — 명령어 한 줄로 설치되고 가정용 인터넷에서도 공인 IP 없이 바로 연결됩니다. 왜 답했는지, 왜 건너뛰었는지, 토큰을 얼마나 썼는지 이벤트 센터에서 한눈에 확인할 수 있습니다.
- **데이터는 내 기기에, 권한은 안전하게 분리** — 설정·기억·로그가 모두 로컬 SQLite에 저장됩니다. **안전 모드**를 켜면 호스트 명령 실행·파일 쓰기·코딩 에이전트를 즉시 차단해 그룹 채팅의 프롬프트 인젝션을 막아 줍니다.

## 3분 만에 시작하기

**① 설치.** 명령어 한 줄을 실행하면 최신 안정 버전을 내려받아 SHA-256 검증 후 바로 시작합니다.

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# Android Termux (ARM64 전용, Termux 내에서 실행, 루팅 불필요)
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Docker (데이터를 저장할 디렉터리에서 실행)
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

네이티브 설치는 관리자 권한이 필요 없습니다. **업그레이드도 같은 명령어를 다시 실행하면 됩니다.** 자세한 내용은 [배포 가이드](https://suink.github.io/Diana/en/deploy.html)를 참고하세요.

**또는 AI 어시스턴트에게 Skill로 설치 맡기기.** 명령 실행이 가능한 AI에게 아래 문장을 붙여 넣으세요(상세 지침은 [설치 Skill](skills/diana-install/SKILL.md)에서 최신 버전과 동기화됩니다):

```text
Read https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md and follow it to install the latest stable release of Diana on this machine.
```

**수동 다운로드.** 스크립트 대신 직접 파일을 받고 싶다면(항상 최신 안정 버전):

| 플랫폼 | 다운로드 | 실행 방법 |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | 별도 폴더에 두고 더블클릭(데이터는 옆 `data` 폴더에 저장) |
| macOS Apple Silicon | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | 압축 해제 후 `./run.sh` 실행 |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | 압축 해제 후 `./run.sh` 실행 |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | 압축 해제 후 `./run.sh` 실행 |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | 압축 해제 후 `./run.sh` 실행 |
| Android (Termux) | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | Termux에서 압축 해제 후 `./run.sh` 실행(앱이 아닌 서버 바이너리) |

WebUI가 바이너리에 내장되어 있어 Node.js가 필요 없습니다. 각 파일의 SHA-256은 [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json)과 [Releases](https://github.com/SuInk/Diana/releases) 페이지에서 확인할 수 있습니다.

**② 콘솔 로그인.** 브라우저에서 `http://127.0.0.1:18080`에 접속합니다. 초기 관리자 아이디와 비밀번호는 설치 시 터미널에 한 번만 표시되므로 즉시 보관하세요(Docker는 `docker logs diana`). 잊어버렸다면 `diana passwd`로 재설정할 수 있습니다.

**③ 설정.** 콘솔에서 세 가지만 설정하면 끝납니다.

1. **LLM 설정**에서 API 키를 입력하고 모델 목록을 동기화해 기본 모델을 선택합니다.
2. **봇** 페이지에서 플랫폼을 선택해 인증 정보를 입력합니다(외부 연동 없이 **내장 봇**으로 바로 시작할 수도 있습니다).
3. 메시지를 보내 테스트합니다. 개인 채팅은 바로 응답하며, 그룹에서는 멘션이나 트리거 단어로 호출할 수 있습니다.

> [!TIP]
> **다른 기기에서 콘솔에 접속하려면:** 기본값은 `127.0.0.1` 전용입니다. 설치 명령어 뒤에 `-s -- --host 0.0.0.0`(Windows는 `-BindHost 0.0.0.0`)을 붙여 다시 실행하고, 외부 공개 시 반드시 HTTPS 리버스 프록시 뒤에 두세요.

## 지원 플랫폼

활성화된 모든 봇 프로필이 동시에 온라인 상태를 유지하며, 응답은 항상 원래 메시지가 온 채널로 돌아갑니다. 가정용 네트워크나 내부망에서도 공인 IP 없이 사용할 수 있습니다.

| 플랫폼 | 필요한 정보 | 연결 방식 |
| --- | --- | --- |
| **OneBot v11** (SnowLuma 권장) | 역방향 WS, 정방향 WS, HTTP API + 이벤트 전송 지원(봇 페이지에서 선택) | 방식에 따라 결정, LAN 환경 지원 |
| **Telegram** | BotFather에서 발급한 Bot Token | Diana 아웃바운드 연결(공인 IP 불필요) |
| **QQ 공식 봇** | 오픈 플랫폼 AppID + AppSecret | Diana 아웃바운드 연결(공인 IP 불필요) |
| **WeChat** | 입력 불필요: 저장 후 ‘연결’에서 QR 코드 스캔(Tencent iLink Bot, 1:1 전용) | Diana 아웃바운드 연결(공인 IP 불필요) |
| **iMessage** | 상시 켜진 Mac의 BlueBubbles Server 주소 및 비밀번호([설정 안내](docs/imessage.md)) | BlueBubbles webhook → Diana(폴링 보조 지원) |
| **내장 봇** | 입력 불필요: 외부 플랫폼 연동 없이 콘솔 ‘채팅’에서 바로 대화 및 음성 통화 | 콘솔 전용, 즉시 체험 가능 |

## 기본 내장 기능

별도 플러그인 설치 없이 단일 바이너리에 포함되어 있으며 콘솔에서 자유롭게 켜고 끌 수 있습니다.

- **자연스러운 능동 대화** — 멘션 없이도 대화 맥락을 보고 참여 여부와 침묵을 스스로 결정하며, 모든 판단 내역이 이벤트 센터에 남습니다.
- **장기 기억 및 노트북** — 계층형 메모리로 토큰 비용을 절감하고, 중요한 약속과 규칙은 검토·수정·복구가 가능한 노트북에 기록합니다.
- **내장 에이전트 & 안전 모드** — 파일·명령어·브라우저 도구와 Skills / MCP 서버를 온디맨드로 불러옵니다. 봇별로 **안전 모드**를 켜면 호스트 명령, 파일 쓰기, 코딩 에이전트를 차단해 프롬프트 인젝션을 방지합니다.
- **코딩 에이전트** — 소유자 전용, 기본 꺼짐. 등록된 저장소에서 로컬 Claude Code / Codex CLI에 장시간 코드 수정과 테스트를 맡길 수 있으며, Diana가 재시작되어도 작업이 이어지고 위험한 작업은 채팅에서 확인을 받습니다.
- **웹 검색** — 기본적으로 샌드박스 브라우저로 병렬 검색을 수행하며, 전역 설정에서 Exa·Tavily 등 검색 공급자를 구성할 수 있습니다.
- **링크 & 파일 분석** — Bilibili / YouTube / X / Xiaohongshu / Douyin 미디어와 X 프로필, PDF / Office / EPUB 문서를 자동으로 읽어 이해합니다.
- **이미지 이해·OCR·다이어그램 렌더링** — 비전 모델과 오프라인 OCR로 텍스트 전용 모델도 이미지를 읽을 수 있으며, 표·플로우차트·시퀀스 다이어그램은 이미지로 렌더링해 전송합니다.
- **노래 신청·스티커·그룹별 정책** — 다중 음원 노래 재생, 대화에서 학습한 스티커 전송, 그룹별 응답 시간대·페르소나·허용/차단 목록·도구 권한 설정을 지원합니다.

자세한 내용은 [설정 문서](https://suink.github.io/Diana/en/configuration.html)를 참고하세요.

## 일상 관리

설치 후 `diana` 명령어가 PATH에 등록됩니다.

```sh
diana status    # 상태, 버전, 주소 확인
diana logs -f   # 실시간 로그 보기
diana restart   # 서비스 재시작
diana doctor    # 설정, 디렉터리, 헬스 체크
```

Docker 배포에서는 호스트에서 `docker exec diana diana <명령>` 형태로 실행합니다.

- **비밀번호 재설정**: 네이티브 설치는 `diana passwd`, Docker는 `docker compose stop diana && docker compose run --rm diana passwd && docker compose start diana`.
- **업그레이드**: 설치 명령어를 다시 실행하거나 콘솔에서 업그레이드를 클릭합니다. 데이터 자동 백업과 헬스 체크 실패 시 자동 롤백을 지원합니다.
- **제거**: `diana uninstall`은 데이터를 보존한 채 프로그램과 서비스를 제거하며, `diana uninstall --purge`는 데이터까지 영구 삭제합니다.

## 보안 안내

- 콘솔은 최초 실행부터 로그인이 필수이며, 출발지 IP별 브루트포스 차단이 적용됩니다.
- 저장된 API 키와 토큰은 평문으로 다시 반환되지 않습니다.
- 리버스 프록시 뒤에 둘 경우 반드시 `config.yaml`의 `trusted_proxies`(또는 환경 변수 `DIANA_TRUSTED_PROXIES`)를 설정하세요.
- 소유자 계정을 설정하면 6자리 일회용 코드로 봇 DM을 통해 빠른 로그인이 가능하며, 접속 IP와 기기가 즉시 알림으로 회신됩니다.

## 기여하기

백엔드는 Go(Gin + SQLite), 프론트엔드는 Vue 3 + TypeScript(`frontend/`)로 구성되어 있습니다.

```sh
make dev        # 백엔드 :18080 + Vite :5173
go test ./...   # 백엔드 테스트
```

PR을 환영합니다. 자세한 개발 규칙은 [CONTRIBUTING.md](./CONTRIBUTING.md)와 [AGENTS.md](./AGENTS.md)를 참고해 주세요.

## 문서

| | |
| --- | --- |
| [배포](https://suink.github.io/Diana/en/deploy.html) | 설치 방법, 서버 배포, 첫 로그인 |
| [설정](https://suink.github.io/Diana/en/configuration.html) | 채널, 모델 할당, 그룹 정책, 보안 경계 |
| [구현](https://suink.github.io/Diana/en/implementation.html) | 아키텍처, 응답 판단 파이프라인, 메모리 계층 |
| [파이프라인](https://suink.github.io/Diana/en/pipeline.html) | 응답 트리거, 에이전트 실행, 브라우저, 발송 전 검토 흐름도 |
| [운영](https://suink.github.io/Diana/en/operations.html) | 업데이트 및 롤백, 로그 백업, 문제 해결 |
| [라이브 데모](https://suink.github.io/Diana/demo/) | 샘플 데이터가 담긴 실제 콘솔 미리보기 |

## 라이선스

`Limited Redistribution License (SuInk)` — 자세한 내용은 [LICENSE](./LICENSE)를 확인하세요.
