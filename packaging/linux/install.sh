#!/bin/sh
# ShareMile installer for Linux.
#   ./install.sh               install for you (no root needed)
#   ./install.sh -y            accept every default, ask nothing
#   ./install.sh --prefix DIR  install the program into DIR
#   sudo ./install.sh          install for everyone (/opt/sharemile)
# Options you can switch off: --no-launch --no-desktop --no-dock --no-github --no-deps
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
YES=0; LAUNCH=1; DESKTOP=1; DOCK=1; GITHUB=1; DEPS=1; PREFIX=""
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) YES=1 ;;
    --prefix) shift; PREFIX=${1:-} ;;
    --prefix=*) PREFIX=${1#--prefix=} ;;
    --no-launch) LAUNCH=0 ;;
    --no-desktop) DESKTOP=0 ;;
    --no-dock) DOCK=0 ;;
    --no-github) GITHUB=0 ;;
    --no-deps) DEPS=0 ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)"; exit 2 ;;
  esac
  shift
done

say() { printf '%s\n' "$*"; }
ask() { # ask "question" -> 0 for yes (the default)
  [ "$YES" = 1 ] && return 0
  [ -t 0 ] || return 0
  printf '%s [Y/n] ' "$1"; read -r a || return 0
  case "$a" in n|N|no|No|NO) return 1 ;; *) return 0 ;; esac
}

[ -f "$HERE/sharemile" ] || { say "sharemile isn't next to this script. Unpack the whole ShareMile folder first."; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) ;;
  *) say "This ShareMile build is for 64-bit Intel/AMD PCs; this computer is $(uname -m)."; exit 1 ;;
esac

if [ "$(id -u)" = 0 ] && [ -z "$PREFIX" ]; then
  SYSTEM=1; PREFIX=/opt/sharemile; BINLINK=/usr/local/bin; APPS=/usr/share/applications; ICONS=/usr/share/icons/hicolor
else
  SYSTEM=0; [ -n "$PREFIX" ] || PREFIX="${XDG_DATA_HOME:-$HOME/.local/share}/sharemile"
  BINLINK="$HOME/.local/bin"; APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"; ICONS="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor"
fi

# ---- system libraries (GTK 3 and WebKitGTK 4.1, the window ShareMile draws in)
missing=$(ldd "$HERE/sharemile" 2>/dev/null | awk '/not found/{print $1}')
if [ -n "$missing" ] && [ "$DEPS" = 1 ]; then
  say "ShareMile needs a few system libraries that aren't installed yet:"
  say "$missing" | sed 's/^/  /'
  . /etc/os-release 2>/dev/null || true
  if command -v pacman >/dev/null; then cmd="pacman -Syu --needed --noconfirm webkit2gtk-4.1 gtk3 xdg-utils" # Arch supports full upgrades only
  elif command -v apt-get >/dev/null; then cmd="apt-get install -y libwebkit2gtk-4.1-0 xdg-utils"
  elif command -v dnf >/dev/null; then cmd="dnf install -y webkit2gtk4.1 gtk3 xdg-utils"
  elif command -v zypper >/dev/null; then cmd="zypper --non-interactive install libwebkit2gtk-4_1-0 gtk3-tools xdg-utils"
  elif command -v xbps-install >/dev/null; then cmd="xbps-install -y webkit2gtk gtk+3 xdg-utils"
  elif command -v apk >/dev/null; then cmd="apk add webkit2gtk-4.1 gtk+3.0 xdg-utils"
  else cmd=""; fi
  if [ -z "$cmd" ]; then
    say "Install WebKitGTK 4.1 and GTK 3 with your package manager, then run this again."
    exit 1
  fi
  if ask "Install them now ($cmd)?"; then
    if [ "$(id -u)" = 0 ]; then sh -c "$cmd"
    elif command -v sudo >/dev/null; then
      case "$cmd" in apt-get*) sudo apt-get update -qq || true ;; esac
      sudo sh -c "$cmd"
    else say "Run as root: $cmd"; exit 1; fi
  fi
  missing=$(ldd "$HERE/sharemile" 2>/dev/null | awk '/not found/{print $1}')
  if [ -n "$missing" ]; then
    say "Still missing: $missing"
    say "Install WebKitGTK 4.1 and GTK 3, then run this again."
    exit 1
  fi
fi

# ---- the program
mkdir -p "$PREFIX" "$BINLINK" "$APPS" "$ICONS/256x256/apps" || exit 1
if pgrep -x -u "$(id -u)" sharemile >/dev/null 2>&1; then say "Closing the running ShareMile…"; pkill -x -u "$(id -u)" sharemile; sleep 1; fi
cp "$HERE/sharemile" "$PREFIX/sharemile.new" && chmod 755 "$PREFIX/sharemile.new" && mv -f "$PREFIX/sharemile.new" "$PREFIX/sharemile" || { say "Couldn't copy the program to $PREFIX"; exit 1; }
cp "$HERE/uninstall.sh" "$PREFIX/uninstall.sh" && chmod 755 "$PREFIX/uninstall.sh"
cp "$HERE/sharemile.png" "$ICONS/256x256/apps/sharemile.png"
ln -sf "$PREFIX/sharemile" "$BINLINK/sharemile"

exe="$PREFIX/sharemile"
case "$exe" in *[\ \"\$\`\\]*) execline="\"$(printf '%s' "$exe" | sed 's/[\\"`$]/\\\\&/g')\"" ;; *) execline="$exe" ;; esac
cat > "$APPS/sharemile.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=ShareMile
GenericName=File Sharing
Comment=Share files over the internet at full line speed
Exec=$execline
Icon=sharemile
Terminal=false
Categories=Network;FileTransfer;
Keywords=share;send;receive;transfer;qr;
StartupWMClass=sharemile
EOF
chmod 644 "$APPS/sharemile.desktop"
command -v update-desktop-database >/dev/null && update-desktop-database -q "$APPS" 2>/dev/null
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t "$ICONS" 2>/dev/null
say "Installed ShareMile in $PREFIX (and in your app menu)."
case ":$PATH:" in *":$BINLINK:"*) ;; *) [ "$SYSTEM" = 0 ] && say "Tip: add $BINLINK to your PATH to start it with the command 'sharemile'." ;; esac

# ---- finish options (all on by default, like the Windows installer)
if [ "$SYSTEM" = 0 ] && [ "$DESKTOP" = 1 ] && ask "Add ShareMile to the desktop?"; then
  "$exe" --desktop-shortcut && say "  Desktop icon added."
fi
if [ "$SYSTEM" = 0 ] && [ "$DOCK" = 1 ] && command -v gsettings >/dev/null && gsettings get org.gnome.shell favorite-apps >/dev/null 2>&1; then
  if ask "Pin ShareMile to the dock?"; then
    fav=$(gsettings get org.gnome.shell favorite-apps)
    case "$fav" in *sharemile.desktop*) ;; "@as []") gsettings set org.gnome.shell favorite-apps "['sharemile.desktop']" ;;
      *) gsettings set org.gnome.shell favorite-apps "$(printf '%s' "$fav" | sed "s/]\$/, 'sharemile.desktop']/")" ;; esac
    say "  Pinned to the dock."
  fi
fi
if [ "$GITHUB" = 1 ] && [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] && ask "Open the project's GitHub page?"; then
  (xdg-open https://github.com/tasin-py/ShareMile >/dev/null 2>&1 &)
fi
if [ "$SYSTEM" = 0 ] && [ "$LAUNCH" = 1 ] && [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ] && ask "Launch ShareMile now?"; then
  (setsid "$exe" >/dev/null 2>&1 < /dev/null &)
  say "  ShareMile is starting."
fi
say "To remove it later: $PREFIX/uninstall.sh"
