#!/usr/bin/env bash
# Installs Piper TTS (same release and voice as the Docker image) plus the
# en_US-lessac-medium model.
#
#   Linux (root):  /opt/piper/piper
#   macOS:         ~/.local/share/piper/piper  (symlink ~/.local/bin/piper)
#                  and: brew install ffmpeg espeak-ng opus-tools
#
# Linux servers normally get this from scripts/install.sh. Run this script
# itself on a Mac, or on Linux when you are not using the full installer.
set -euo pipefail

PIPER_VERSION="${PIPER_VERSION:-2023.11.14-2}"
PIPER_MODEL="${PIPER_MODEL:-en_US-lessac-medium}"
PIPER_MODEL_URL="https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/${PIPER_MODEL}.onnx"

if [ -t 1 ]; then
	G=$'\033[32m' Y=$'\033[33m' R=$'\033[31m' N=$'\033[0m'
else
	G="" Y="" R="" N=""
fi
ok() { printf '    %s✓%s %s\n' "$G" "$N" "$1"; }
warn() { printf '    %s!%s %s\n' "$Y" "$N" "$1" >&2; }
die() { printf '%sError:%s %s\n' "$R" "$N" "$1" >&2; exit 1; }

usage() {
	cat <<EOF
Usage: install-piper.sh [--prefix DIR]

Installs Piper ${PIPER_VERSION} and the ${PIPER_MODEL} voice.

  --prefix DIR   Install directory (default: /opt/piper as root on Linux,
                 ~/.local/share/piper otherwise)
EOF
}

# install_piper downloads Piper and the default voice into $PIPER_PREFIX.
install_piper() {
	local os arch asset prefix bindir link
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
	Linux)
		case "$arch" in
		x86_64 | amd64) asset=piper_linux_x86_64.tar.gz ;;
		aarch64 | arm64) asset=piper_linux_aarch64.tar.gz ;;
		armv7l | armv7*) asset=piper_linux_armv7l.tar.gz ;;
		*) die "unsupported CPU: $arch" ;;
		esac
		;;
	Darwin)
		case "$arch" in
		arm64) asset=piper_macos_aarch64.tar.gz ;;
		x86_64) asset=piper_macos_x64.tar.gz ;;
		*) die "unsupported CPU: $arch" ;;
		esac
		;;
	*)
		die "this script supports Linux and macOS. On Windows run scripts/install-piper.ps1."
		;;
	esac

	if [ -n "${PIPER_PREFIX:-}" ]; then
		prefix=$PIPER_PREFIX
	elif [ "$os" = Linux ] && [ "$(id -u)" = 0 ]; then
		prefix=/opt/piper
	else
		prefix=${HOME}/.local/share/piper
	fi

	if [ "$os" = Darwin ]; then
		install_macos_brew_deps
		if [ "$arch" = arm64 ]; then
			warn "Piper ${PIPER_VERSION}'s macOS arm64 archive is an Intel binary. Apple Silicon Macs run it through Rosetta 2."
		fi
	fi

	if [ -x "$prefix/piper" ] && [ -f "$prefix/models/${PIPER_MODEL}.onnx" ] && [ -f "$prefix/models/${PIPER_MODEL}.onnx.json" ]; then
		ok "Piper is already installed at $prefix"
	else
		local url="https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/${asset}"
		local work
		work=$(mktemp -d)
		printf '    Downloading %s\n' "$asset"
		curl -fL --retry 3 --retry-delay 2 -o "$work/piper.tgz" "$url" || die "download failed: $url"
		tar -xzf "$work/piper.tgz" -C "$work"
		[ -x "$work/piper/piper" ] || die "archive did not contain piper/piper"
		mkdir -p "$prefix/models"
		# Keep the libraries and espeak-ng-data beside the binary ($ORIGIN / @loader_path).
		cp -a "$work/piper/." "$prefix/"
		chmod 755 "$prefix/piper"
		printf '    Downloading voice %s\n' "$PIPER_MODEL"
		curl -fL --retry 3 --retry-delay 2 -o "$prefix/models/${PIPER_MODEL}.onnx" "$PIPER_MODEL_URL" ||
			die "voice download failed: $PIPER_MODEL_URL"
		curl -fL --retry 3 --retry-delay 2 -o "$prefix/models/${PIPER_MODEL}.onnx.json" "${PIPER_MODEL_URL}.json" ||
			die "voice config download failed: ${PIPER_MODEL_URL}.json"
		rm -rf "$work"
		if [ "$(id -u)" = 0 ]; then
			chmod -R a+rX "$prefix"
		fi
		ok "Installed Piper ${PIPER_VERSION} at $prefix"
	fi

	if [ "$os" = Linux ] && [ "$(id -u)" = 0 ]; then
		bindir=/usr/local/bin
	else
		bindir=${HOME}/.local/bin
	fi
	mkdir -p "$bindir"
	link=$bindir/piper
	ln -sfn "$prefix/piper" "$link"
	ok "Linked $link"

	cat <<EOF

    Add this to config.toml (create the file with the setup wizard if it does not exist yet):

    [tts]
    piper_binary = "${prefix}/piper"
    piper_model = "${prefix}/models/${PIPER_MODEL}.onnx"
    opusenc_binary = "opusenc"

EOF
}

install_macos_brew_deps() {
	if ! command -v brew >/dev/null 2>&1; then
		die "Homebrew is required on macOS. Install it from https://brew.sh then re-run this script."
	fi
	printf '    Installing ffmpeg, espeak-ng and opus-tools with Homebrew\n'
	brew install ffmpeg espeak-ng opus-tools
	ok "Homebrew dependencies installed"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	while [ $# -gt 0 ]; do
		case "$1" in
		--prefix)
			PIPER_PREFIX="${2:-}"
			[ -n "$PIPER_PREFIX" ] || die "--prefix needs a directory"
			shift
			;;
		-h | --help) usage; exit 0 ;;
		*) die "unknown option: $1" ;;
		esac
		shift
	done
	install_piper
fi
