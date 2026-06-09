---
name: build-and-run
description: Build and run the OpenUAI Wails desktop app locally on Windows (git-bash). Use when asked to compile, build, launch, run, or relaunch the app, or to test a change in the real app. Covers the toolchain (Go, Wails CLI, gcc/MinGW for CGO) and the CGO_ENABLED=1 build that the app requires (malgo mic capture + local whisper).
---

# Build & run OpenUAI (Windows / git-bash)

OpenUAI is a **Wails v2 (Go)** desktop app. It **requires CGO** (the `malgo`
mic-capture binding and the local whisper integration), so a C compiler must be
on PATH and `CGO_ENABLED=1` — a plain `wails build` with CGO off fails with
`undefined: malgo.*`.

## Toolchain (install once)

All installed via `winget` on this machine:

| Tool | winget id | Verify |
|---|---|---|
| Go | `GoLang.Go` | `go version` |
| Wails CLI | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` | `wails version` |
| gcc / MinGW-w64 | `BrechtSanders.WinLibs.POSIX.UCRT` | `gcc --version` |
| Node (frontend) | already present via fnm | `node -v` |

These are **not on the default PATH** in git-bash. Export them at the top of
every build/run shell:

```bash
GCCBIN="/c/Users/hugo/AppData/Local/Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe/mingw64/bin"
export PATH="$GCCBIN:$PATH:/c/Program Files/Go/bin:/c/Users/hugo/go/bin"
export CGO_ENABLED=1
```

(If the WinLibs package path changed, find gcc with:
`find "/c/Users/hugo/AppData/Local/Microsoft/WinGet/Packages" -name gcc.exe`)

## Build

```bash
cd /c/Users/hugo/projects/openuai
GCCBIN="/c/Users/hugo/AppData/Local/Microsoft/WinGet/Packages/BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe/mingw64/bin"
export PATH="$GCCBIN:$PATH:/c/Program Files/Go/bin:/c/Users/hugo/go/bin"
export CGO_ENABLED=1
version=$(git describe --tags --always)
wails build -o openuai.exe -ldflags "-X main.Version=$version"
```

Output: `build/bin/openuai.exe` (~17 MB). First build ~1–2 min (compiles
whisper/malgo C); incremental ~13 s. The `go.mod uses Wails 2.11.0 but CLI is
2.12.0` warning is harmless.

## Run / relaunch

Always kill the previous instance first — a stale process keeps the OAuth
callback port (1455) and the WebView2 window:

```bash
cd /c/Users/hugo/projects/openuai
taskkill.exe //IM openuai.exe //F 2>/dev/null; sleep 1
./build/bin/openuai.exe &
sleep 4
tasklist.exe | grep -i openuai   # confirm it's up
```

Launch is OK when you see `[WebView2] Environment created successfully`. The two
`systray error: ... tray not ready yet` lines on startup are benign.

## Logs

Daily log file — read it to diagnose runtime errors (OAuth, whisper, MCP, …):

```
/c/Users/hugo/AppData/Roaming/openuai/logs/$(date +%Y-%m-%d).log
```

Config + tokens live in `/c/Users/hugo/AppData/Roaming/openuai/`.

## Notes

- This skill is for **local dev builds**. Official multi-platform releases are
  built by CI (`.github/workflows/release.yml`) on pushing a `v*` tag.
- `wails build` with the v2.12.0 CLI regenerates `frontend/wailsjs/*` and may
  touch line endings in `go.mod` — those are generated artifacts; don't commit
  them with feature changes (`git checkout -- frontend/ go.mod`).
