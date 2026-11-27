#!/bin/sh
# One-line ShareMile installer for Linux:
#   curl -fsSL https://raw.githubusercontent.com/tasin-py/ShareMile/main/get.sh | sh
# Options are passed to install.sh, for example:
#   curl -fsSL https://raw.githubusercontent.com/tasin-py/ShareMile/main/get.sh | sh -s -- -y
# It downloads the latest release, unpacks it in a temporary folder, runs
# install.sh from there and deletes the temporary folder.
set -u

main() {
  url="https://github.com/tasin-py/ShareMile/releases/latest/download/ShareMile-linux-amd64.tar.gz"
  case "$(uname -s)" in Linux) ;; *) echo "This installer is for Linux. Windows users: get ShareMile-Setup.exe from https://github.com/tasin-py/ShareMile/releases/latest"; exit 1 ;; esac
  case "$(uname -m)" in x86_64|amd64) ;; *) echo "ShareMile for Linux needs a 64-bit Intel/AMD PC; this one is $(uname -m)."; exit 1 ;; esac
  command -v tar >/dev/null || { echo "Install tar first."; exit 1; }

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t sharemile) || { echo "Couldn't make a temporary folder."; exit 1; }
  trap 'rm -rf "$tmp"' EXIT INT TERM HUP

  echo "Downloading ShareMile…"
  if command -v curl >/dev/null; then
    curl -fL --progress-bar "$url" -o "$tmp/sm.tar.gz" || { echo "Download failed. Check your internet connection and try again."; exit 1; }
  elif command -v wget >/dev/null; then
    wget -q --show-progress -O "$tmp/sm.tar.gz" "$url" || wget -q -O "$tmp/sm.tar.gz" "$url" || { echo "Download failed. Check your internet connection and try again."; exit 1; }
  else
    echo "Install curl or wget first."; exit 1
  fi
  tar -xzf "$tmp/sm.tar.gz" -C "$tmp" || { echo "The download was damaged. Try again."; exit 1; }
  [ -x "$tmp/ShareMile/install.sh" ] || { echo "The download didn't contain install.sh. Try again."; exit 1; }

  # The script itself arrives on stdin, so give install.sh the keyboard for its questions.
  if [ -r /dev/tty ] && (exec </dev/tty) 2>/dev/null; then
    sh "$tmp/ShareMile/install.sh" "$@" </dev/tty
  else
    sh "$tmp/ShareMile/install.sh" "$@"
  fi
}

# Everything runs from main, so a download cut off halfway runs nothing.
main "$@"
