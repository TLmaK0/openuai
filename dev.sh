#!/usr/bin/env bash
set -e

# Detect OS and install system dependencies
install_deps() {
  case "$(uname -s)" in
    Linux)
      # Already installed: skip sudo, so unattended relaunches never block
      # waiting for a password.
      if pkg-config --exists webkit2gtk-4.1 2>/dev/null || pkg-config --exists webkit2gtk-4.0 2>/dev/null; then
        echo "→ webkit2gtk already installed"
        return
      fi
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
    Darwin)
      install_deps_macos
      ;;
    MINGW*|MSYS*)
      echo "→ No system deps needed on Windows"
      ;;
  esac
}

# macOS: Xcode Command Line Tools (clang for CGO), Go and Node/npm (frontend
# build). Missing tools are installed with Homebrew.
install_deps_macos() {
  if ! xcode-select -p &>/dev/null; then
    echo "→ Installing Xcode Command Line Tools..."
    xcode-select --install || true
    echo "⚠ Finish the Xcode Command Line Tools installer, then run ./dev.sh again"
    exit 1
  fi

  local missing=()
  command -v go &>/dev/null || missing+=(go)
  command -v npm &>/dev/null || missing+=(node)
  if [[ ${#missing[@]} -eq 0 ]]; then
    echo "→ Xcode CLT, Go and Node already installed"
    return
  fi

  if ! command -v brew &>/dev/null; then
    for prefix in /opt/homebrew /usr/local; do
      if [[ -x "$prefix/bin/brew" ]]; then
        eval "$("$prefix/bin/brew" shellenv)"
      fi
    done
  fi
  if ! command -v brew &>/dev/null; then
    echo "⚠ Missing: ${missing[*]}. Install Homebrew (https://brew.sh) or install them manually, then run ./dev.sh again"
    exit 1
  fi
  echo "→ Installing ${missing[*]} (brew)..."
  brew install "${missing[@]}"
}

# Install Go tools
install_go_tools() {
  # go install puts binaries here; it is often missing from PATH.
  export PATH="$PATH:$(go env GOPATH)/bin"
  if ! command -v wails &>/dev/null; then
    echo "→ Installing Wails CLI..."
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
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
  # On macOS wails build produces an .app bundle
  if [[ "$(uname -s)" == "Darwin" ]]; then
    bin="./build/bin/openuai.app/Contents/MacOS/openuai"
  fi
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
