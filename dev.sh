#!/usr/bin/env bash
set -e

# Detect OS and install system dependencies
install_deps() {
  case "$(uname -s)" in
    Linux)
      if command -v apt-get &>/dev/null; then
        echo "→ Installing webkit2gtk (apt)..."
        sudo apt-get install -y libwebkit2gtk-4.0-dev libgtk-3-dev 2>/dev/null || \
        sudo apt-get install -y libwebkit2gtk-4.1-dev libgtk-3-dev
      elif command -v dnf &>/dev/null; then
        echo "→ Installing webkit2gtk (dnf)..."
        sudo dnf install -y webkit2gtk4.1-devel gtk3-devel 2>/dev/null || \
        sudo dnf install -y webkit2gtk4.0-devel gtk3-devel
      elif command -v pacman &>/dev/null; then
        echo "→ Installing webkit2gtk (pacman)..."
        sudo pacman -S --noconfirm webkit2gtk-4.1 gtk3 2>/dev/null || \
        sudo pacman -S --noconfirm webkit2gtk gtk3
      else
        echo "⚠ Unknown package manager — install webkit2gtk manually"
      fi
      ;;
    Darwin|MINGW*|MSYS*)
      echo "→ No system deps needed on macOS/Windows"
      ;;
  esac
}

# Install Go tools
install_go_tools() {
  if ! command -v wails &>/dev/null; then
    echo "→ Installing Wails CLI..."
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
  fi
}

# Detect webkit build tag for Linux
webkit_tag() {
  if pkg-config --exists webkit2gtk-4.1 2>/dev/null; then
    echo "webkit2_41"
  else
    echo "webkit2_40"
  fi
}

# Build
build() {
  local tag
  tag=$(webkit_tag)
  local version
  version=$(git describe --tags --always 2>/dev/null || echo "dev")
  echo "→ Building $version (tags: $tag)..."
  wails build -tags "$tag" -o openuai -ldflags "-X main.Version=$version"
}

# Run
run() {
  local bin="./build/bin/openuai"
  echo "→ Launching $bin ..."
  if [[ "$(uname -s)" == "Linux" && -z "$DISPLAY" ]]; then
    export DISPLAY=:1
  fi
  exec "$bin"
}

echo "=== OpenUAI dev setup ==="
install_deps
install_go_tools
build
run
