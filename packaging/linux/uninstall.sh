#!/bin/sh
# Removes ShareMile. Your settings and downloads stay unless you say so.
#   ./uninstall.sh       ask before removing settings
#   ./uninstall.sh -y    remove the program, keep settings and downloads
#   ./uninstall.sh --purge   also remove settings (~/.config/ShareMile)
set -u
YES=0; PURGE=0
for a in "$@"; do case "$a" in -y|--yes) YES=1 ;; --purge) PURGE=1; YES=1 ;; esac; done

if [ "$(id -u)" = 0 ] && [ -d /opt/sharemile ]; then
  PREFIX=/opt/sharemile; BINLINK=/usr/local/bin; APPS=/usr/share/applications; ICONS=/usr/share/icons/hicolor
else
  PREFIX=$(cd "$(dirname "$0")" && pwd)
  [ -f "$PREFIX/sharemile" ] || PREFIX="${XDG_DATA_HOME:-$HOME/.local/share}/sharemile"
  BINLINK="$HOME/.local/bin"; APPS="${XDG_DATA_HOME:-$HOME/.local/share}/applications"; ICONS="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor"
fi

pgrep -x -u "$(id -u)" sharemile >/dev/null 2>&1 && { pkill -x -u "$(id -u)" sharemile; sleep 1; }
rm -f "$PREFIX/sharemile" "$PREFIX/uninstall.sh" "$APPS/sharemile.desktop" "$ICONS/256x256/apps/sharemile.png"
[ -L "$BINLINK/sharemile" ] && rm -f "$BINLINK/sharemile"
rmdir "$PREFIX" 2>/dev/null
desk=$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")
rm -f "$desk/sharemile.desktop"
if command -v gsettings >/dev/null && fav=$(gsettings get org.gnome.shell favorite-apps 2>/dev/null); then
  case "$fav" in *sharemile.desktop*)
    gsettings set org.gnome.shell favorite-apps "$(printf '%s' "$fav" | sed "s/, 'sharemile.desktop'//; s/'sharemile.desktop', //; s/'sharemile.desktop'//")" ;;
  esac
fi
command -v update-desktop-database >/dev/null && update-desktop-database -q "$APPS" 2>/dev/null
echo "ShareMile was removed."

cfg="${XDG_CONFIG_HOME:-$HOME/.config}/ShareMile"
if [ -d "$cfg" ]; then
  if [ "$PURGE" = 1 ]; then rm -rf "$cfg"; echo "Settings removed."
  elif [ "$YES" = 0 ] && [ -t 0 ]; then
    printf 'Also remove your settings, networks and download list (%s)? [y/N] ' "$cfg"; read -r a
    case "$a" in y|Y|yes) rm -rf "$cfg"; echo "Settings removed." ;; *) echo "Settings kept." ;; esac
  fi
fi
echo "Files you downloaded were not touched."
