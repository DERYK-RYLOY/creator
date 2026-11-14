#!/bin/sh
# Creator — one-line installer.
#
#   curl -fsSL https://raw.githubusercontent.com/DERYK-RYLOY/creator/main/install.sh | sh
#
# Pin a version, skip PATH edits, or try a custom directory:
#   CREATOR_VERSION=v0.1.0 sh install.sh
#   CREATOR_INSTALL_DIR="$HOME/bin" sh install.sh
#   sh install.sh --version v0.1.0 --prefix "$HOME/tools/creator" --no-path --dry-run
#   sh install.sh --from-source
#
# No root, no sudo, no network calls except GitHub itself. Everything it
# changes is printed, and the script is idempotent — run it twice, nothing
# breaks. Works on macOS, Linux (glibc and musl) and from Git Bash on Windows.
set -eu

REPO="DERYK-RYLOY/creator"
PROJECT="creator"
INSTALLER_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"
API_LATEST="https://api.github.com/repos/$REPO/releases/latest"
DOWNLOAD_BASE="https://github.com/$REPO/releases/download"

# ---------- pretty ----------
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
  IS_TTY=1
else
  IS_TTY=0
fi
say()  { printf '%s\n' "$*"; }
info() { if [ "$IS_TTY" = 1 ]; then printf '\033[36m◆\033[0m %s\n' "$*"; else printf '◆ %s\n' "$*"; fi; }
ok()   { if [ "$IS_TTY" = 1 ]; then printf '\033[32m✓\033[0m %s\n' "$*"; else printf '✓ %s\n' "$*"; fi; }
warn() { if [ "$IS_TTY" = 1 ]; then printf '\033[33m!\033[0m %s\n' "$*" >&2; else printf '! %s\n' "$*" >&2; fi; }
fail() { if [ "$IS_TTY" = 1 ]; then printf '\033[31m✗\033[0m %s\n' "$*" >&2; else printf '✗ %s\n' "$*" >&2; fi; exit 1; }

# ---------- args ----------
VERSION="${CREATOR_VERSION:-latest}"
BINDIR="${CREATOR_INSTALL_DIR:-$HOME/.local/bin}"
FROM_SOURCE=0
DRY_RUN=0
NO_PATH=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version)   [ $# -ge 2 ] || fail "--version needs a value (e.g. v0.1.0)"; VERSION="$2"; shift 2 ;;
    --prefix)    [ $# -ge 2 ] || fail "--prefix needs a directory"; BINDIR="$2"; shift 2 ;;
    --from-source) FROM_SOURCE=1; shift ;;
    --no-path)   NO_PATH=1; shift ;;
    --dry-run)   DRY_RUN=1; shift ;;
    -h|--help)
      say "usage: install.sh [--version vX.Y.Z] [--prefix DIR] [--from-source] [--no-path] [--dry-run]"
      exit 0 ;;
    *) fail "unknown option: $1 (try --help)" ;;
  esac
done

run() { # respects --dry-run for everything that changes the machine
  if [ "$DRY_RUN" = 1 ]; then say "  [dry-run] $*"; else "$@"; fi
}

TMPDIR_CREATOR=""
cleanup() { [ -n "$TMPDIR_CREATOR" ] && [ -d "$TMPDIR_CREATOR" ] && rm -rf -- "$TMPDIR_CREATOR" || true; }
trap cleanup EXIT HUP INT TERM

# ---------- detect os / arch ----------
os=$(uname -s 2>/dev/null || echo unknown)
arch=$(uname -m 2>/dev/null || echo unknown)
case "$os" in
  Linux*)  GOOS=linux ;;
  Darwin*) GOOS=darwin ;;
  MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
  *) fail "unsupported OS: $os (this installer covers Linux, macOS and Git Bash on Windows)" ;;
esac
case "$arch" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) fail "unsupported architecture: $arch (amd64 and arm64 are built)" ;;
esac

# ---------- resolve version ----------
info "creator installer — repo $REPO"
if [ "$FROM_SOURCE" = 0 ]; then
  if [ "$VERSION" = "latest" ]; then
    command -v curl >/dev/null 2>&1 && dl_cmd="curl" || dl_cmd=$(command -v wget || true)
    [ -n "$dl_cmd" ] || fail "neither curl nor wget found — install one, or use --from-source"
    info "resolving the latest release…"
    api_body=$(curl -fsSL "$API_LATEST" 2>/dev/null || true)
    if [ -z "$api_body" ] && command -v wget >/dev/null 2>&1; then
      api_body=$(wget -qO- "$API_LATEST" 2>/dev/null || true)
    fi
    TAG=$(printf '%s' "$api_body" | grep -o '"tag_name"[^,]*' | head -n1 | cut -d'"' -f4)
    if [ -z "$TAG" ]; then
      warn "could not read the latest release from GitHub — falling back to --from-source"
      FROM_SOURCE=1
    else
      VERSION="$TAG"
    fi
  fi
fi
VERSION_NUM="${VERSION#v}"
if [ "$FROM_SOURCE" = 1 ]; then
  info "building from source (git + go required)"
else
  info "installing $VERSION_NUM for $GOOS/$GOARCH"
fi
info "target: $BINDIR"
[ "$DRY_RUN" = 1 ] && warn "dry run — nothing will be changed"
[ "$NO_PATH" = 1 ] && warn "PATH will not be modified"

# ---------- checksum helper ----------
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1;   then shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1;  then openssl dgst -sha256 "$1" | sed 's/^.*= //'
  else return 1
  fi
}
fetch() { # fetch <url> <dest>
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  else wget -q "$1" -O "$2"
  fi
}
fetch_stdout() { # fetch_stdout <url>
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1"
  else wget -qO- "$1"
  fi
}

TMPDIR_CREATOR=$(mktemp -d 2>/dev/null || mktemp -d -t creator-install)

# ---------- from source ----------
install_from_source() {
  command -v git >/dev/null 2>&1 || fail "git not found — install git, or download a release instead"
  command -v go  >/dev/null 2>&1 || fail "go not found — install Go 1.27+, or download a release instead"
  info "cloning $REPO (depth 1)"
  run git clone --depth 1 "https://github.com/$REPO.git" "$TMPDIR_CREATOR/creator"
  info "building (go build -trimpath -ldflags '-s -w')"
  mkdir -p "$BINDIR"
  for t in creator creator-tui ctr; do
    out="$TMPDIR_CREATOR/$t"
    [ "$GOOS" = windows ] && out="$out.exe"
    if [ "$DRY_RUN" = 1 ]; then
      say "  [dry-run] (cd $TMPDIR_CREATOR/creator && go build -trimpath -ldflags '-s -w -X ...Version=$VERSION_NUM' -o $out ./cmd/$t)"
    else
      ( cd "$TMPDIR_CREATOR/creator" && go build -trimpath -ldflags "-s -w -X github.com/DERYK-RYLOY/creator/internal/buildinfo.Version=$VERSION_NUM" -o "$out" "./cmd/$t" ) \
        || fail "build of $t failed"
      cp "$out" "$BINDIR/" || fail "could not write $BINDIR/$(basename "$out")"
      chmod 755 "$BINDIR/$(basename "$out")" || true
    fi
  done
}

# ---------- release archive ----------
install_release() {
  base="$DOWNLOAD_BASE/$VERSION"
  ext=tar.gz
  [ "$GOOS" = windows ] && ext=zip
  archive="creator-$VERSION_NUM-$GOOS-$GOARCH.$ext"
  url="$base/$archive"
  info "downloading $archive"
  run fetch "$url" "$TMPDIR_CREATOR/$archive"
  info "verifying SHA-256"
  expected=$(fetch_stdout "$base/checksums.txt" 2>/dev/null | grep " $archive\$" | head -n1 | cut -d' ' -f1 || true)
  if [ -z "$expected" ]; then
    warn "checksums.txt missing or unreadable from $base — installing without verification"
  else
    actual=$(sha256_of "$TMPDIR_CREATOR/$archive") || fail "no SHA-256 tool found (sha256sum, shasum or openssl required)"
    if [ "$actual" != "$expected" ]; then
      rm -f "$TMPDIR_CREATOR/$archive"
      fail "checksum mismatch for $archive — expected $expected, got $actual (aborting, nothing installed)"
    fi
    ok "checksum verified"
  fi
  info "unpacking to $BINDIR"
  if [ "$GOOS" = windows ]; then
    if ! command -v unzip >/dev/null 2>&1; then fail "unzip not found — install it, or use install.ps1 on Windows"; fi
    run mkdir -p "$BINDIR"
    run unzip -oq "$TMPDIR_CREATOR/$archive" -d "$TMPDIR_CREATOR/unpacked"
  else
    run mkdir -p "$TMPDIR_CREATOR/unpacked" "$BINDIR"
    run tar -xzf "$TMPDIR_CREATOR/$archive" -C "$TMPDIR_CREATOR/unpacked"
  fi
  # copy the three binaries (archive contains creator{,.exe}, creator-tui{,.exe}, ctr{,.exe})
  found=0
  for b in creator creator-tui ctr; do
    src="$TMPDIR_CREATOR/unpacked/$b"
    [ -f "$src" ] || src="$TMPDIR_CREATOR/unpacked/$b.exe"
    if [ -f "$src" ]; then
      run cp "$src" "$BINDIR/"
      run chmod 755 "$BINDIR/$(basename "$src")"
      found=$((found + 1))
    fi
  done
  [ "$found" -ge 1 ] || fail "archive did not contain a creator binary — refusing to install"
  ok "installed $found binaries"
}

if [ "$FROM_SOURCE" = 1 ]; then
  install_from_source
else
  install_release
fi

# ---------- PATH setup ----------
path_line_bash='export PATH="$HOME/.local/bin:$PATH"'
add_to_rc() { # add_to_rc <file> <shell-name>
  rc="$1"; name="$2"
  [ -f "$rc" ] || return 0
  if grep -q '# >>> creator >>>' "$rc" 2>/dev/null; then
    info "PATH already configured in $rc — leaving it alone"
    return 0
  fi
  run printf '\n# >>> creator >>>\nexport PATH="%s:$PATH"\n# <<< creator <<<\n' "$BINDIR" >> "$rc"
  ok "PATH updated in $rc (open a new terminal, or: . $rc)"
}
if [ "$NO_PATH" = 0 ]; then
  case ":$PATH:" in
    *":$BINDIR:"*) info "$BINDIR is already on your PATH" ;;
    *)
      if [ "$GOOS" = windows ]; then
        warn "add $BINDIR to your PATH (Git Bash usually inherits the Windows PATH), then run: creator"
      else
        shell_name=$(basename "${SHELL:-sh}")
        case "$shell_name" in
          zsh)  add_to_rc "$HOME/.zshrc" zsh ;;
          bash) add_to_rc "$HOME/.bashrc" bash
                [ -f "$HOME/.bash_profile" ] && add_to_rc "$HOME/.bash_profile" bash-profile ;;
          fish) if command -v fish >/dev/null 2>&1; then
                  mkdir -p "$HOME/.config/fish"
                  touch "$HOME/.config/fish/config.fish"
                  if ! grep -q 'fish_add_path.*creator\|fish_user_paths.*local/bin' "$HOME/.config/fish/config.fish" 2>/dev/null; then
                    run printf '# >>> creator >>>\nfish_add_path "%s"\n# <<< creator <<<\n' "$BINDIR" >> "$HOME/.config/fish/config.fish"
                    ok "PATH updated in fish config"
                  fi
                fi ;;
          *)    add_to_rc "$HOME/.profile" profile ;;
        esac
        # a POSIX fallback so login shells see it everywhere
        [ -f "$HOME/.profile" ] && ! grep -q '# >>> creator >>>' "$HOME/.profile" && run printf '\n# >>> creator >>>\nexport PATH="%s:$PATH"\n# <<< creator <<<\n' "$BINDIR" >> "$HOME/.profile"
      fi
      ;;
  esac
fi

# ---------- done ----------
printf '\n'
if [ "$IS_TTY" = 1 ]; then
  printf '\033[1;36m◆ CREATOR\033[0m \033[2minstalled — the terminal agent that never floods its context\033[0m\n'
else
  printf '◆ CREATOR installed — the terminal agent that never floods its context\n'
fi
printf '\n'
printf '  open it:      creator\n'
printf '  headless:     creator run "why is the build failing?"\n'
printf '  connect a key: creator connect <provider>     (or: export ANTHROPIC_API_KEY=…)\n'
printf '  no key yet:   pollinations is free, no key — creator run --provider pollinations "hello"\n'
printf '\n'
if [ "$NO_PATH" = 0 ] && ! command -v creator >/dev/null 2>&1 && [ "$GOOS" != windows ]; then
  printf '  one more step: restart your terminal, or run: export PATH="%s:$PATH"\n' "$BINDIR"
fi
printf '  docs: https://github.com/%s\n\n' "$REPO"
exit 0
