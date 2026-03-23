# OpenUAI — Open Unmanned Artificial Intelligence

> **[Website](https://openuai.pages.dev)** · **[Download](https://github.com/TLmaK0/openuai/releases/latest)** · **[Issues](https://github.com/TLmaK0/openuai/issues)**

Autonomous AI agent that reacts to events, executes tasks, and runs as a single binary on any OS. Full system access — file management, shell execution, git operations, web browsing, voice, and more.

## Features

- **Autonomous agent** — plan-first execution with full tool access: bash, filesystem, git, web
- **Event-driven** — subscribe to WhatsApp, Teams, email, webhooks; agent reacts automatically
- **Multi-agent** — spawn concurrent sub-agents for parallel task execution
- **MCP compatible** — connect to any MCP server to extend capabilities
- **Voice** — push-to-talk with Whisper STT (auto-detect language) + TTS
- **System tray** — background mode with native OS notifications
- **REST API** — run headless with 18 endpoints + WebSocket
- **Cost tracking** — real-time token usage and cost per request
- **Single binary** — no Docker, no Node, no Python. Download and run.

## Install

Download the latest binary from [Releases](https://github.com/TLmaK0/openuai/releases/latest) for your platform:

| Platform | Download |
|----------|----------|
| Linux amd64 | `openuai-linux-amd64` |
| Linux arm64 | `openuai-linux-arm64` |
| macOS universal | `openuai-macos-universal.zip` |
| Windows amd64 | `openuai-windows-amd64.exe` |

### Linux

Requires `libwebkit2gtk-4.1` (pre-installed on most desktop distributions):

```bash
# Ubuntu/Debian
sudo apt install libwebkit2gtk-4.1-0 libgtk-3-0

# Fedora
sudo dnf install webkit2gtk4.1 gtk3

# Arch
sudo pacman -S webkit2gtk-4.1 gtk3
```

### macOS

No additional dependencies. Uses native WKWebView.

### Windows

No additional dependencies. Uses WebView2 (included with Windows 10+).

## Development

Clone and run in one command:

```bash
git clone https://github.com/TLmaK0/openuai.git
cd openuai
./dev.sh
```

`dev.sh` installs system dependencies (webkit2gtk), installs the Wails CLI if missing, builds, and launches the app.

### Manual build

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Linux (Ubuntu/Debian with webkit2gtk-4.0)
wails build -tags webkit2_40

# Linux (webkit2gtk-4.1) / macOS / Windows
wails build
```

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    openuai (single binary)                    │
│                                                              │
│  [Event Sources]        [Engine]            [Actions]        │
│   WhatsApp ──┐                             ┌── Filesystem    │
│   Email ─────┤                             ├── Reply         │
│   Webhooks ──┤                             ├── Bash/Git      │
│   Teams ─────┼→ Event Bus → LLM ────┐     ├── APIs          │
│   Slack ─────┤              Agent   ├→ Agent├── Voice Out    │
│   Cron ──────┤                      │      └── Notify        │
│   Clipboard ─┘                      │                        │
│                                     ├→ Sub-Agent (parallel)  │
│                                     └→ Sub-Agent (parallel)  │
│                                                              │
│  [System Tray]  [Embedded UI]  [LLM Client]  [Cost Tracker] │
│  [Memory]       [MCP Client/Server]  [REST API]  [Voice In] │
└──────────────────────────────────────────────────────────────┘
```

## License

Commons Clause + MIT
