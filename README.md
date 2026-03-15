# OpenUAI

Autonomous AI agent with full system access — file management, shell execution, git operations, web browsing, and more. Powered by OpenAI and Claude APIs.

## Install

Download the latest binary from [Releases](https://github.com/singular-aircraft/openuai/releases) and run it.

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

## Build from source

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Build
wails build
```

## Features

- Chat with AI agents (OpenAI Codex / Claude)
- Agent executes tools autonomously: bash, filesystem, git, web fetch
- Permission system: allow once / per session / forever
- Session persistence across restarts
- Real-time cost tracking
- OpenAI OAuth login (ChatGPT subscription) or Claude API key

## License

Commons Clause + MIT
