#!/usr/bin/env bash
# Installs or upgrades whatomate on Linux: the binary, a systemd service, the
# firewall rules and, optionally, a reverse proxy for your domain.
#
#   curl -fsSL https://raw.githubusercontent.com/trysoft-team/whatomate/main/scripts/install.sh | sudo bash
#
# Run with --help for the options.
set -euo pipefail

REPO="trysoft-team/whatomate"
BIN=/usr/local/bin/whatomate
UNIT=/etc/systemd/system/whatomate.service
SVC_USER=whatomate
CONFIG_DIR=/etc/whatomate
DATA_DIR=/var/lib/whatomate
FILES_DIR=/var/whatomate
APP_PORT=4747
UDP_MIN=10000
UDP_MAX=10100

VERSION=""
DOMAIN=""
PROXY=""
EMAIL=""
ASSUME_YES=0
DO_FIREWALL=1
DO_VOICE=1
DO_TLS=""

usage() {
	cat <<EOF
Usage: install.sh [options]

  --version X.Y.Z   Install this release (default: the latest)
  --domain NAME     Domain for the reverse proxy, e.g. chat.example.com
  --proxy NAME      apache, nginx, caddy or none (default: ask / detect)
  --email ADDRESS   Email for the Let's Encrypt certificate
  --no-tls          Don't request a Let's Encrypt certificate
  --no-firewall     Don't change firewall rules
  --no-voice        Don't install ffmpeg, opus-tools and espeak-ng
  -y, --yes         Use the defaults instead of asking
  -h, --help        Show this help
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version) VERSION="${2#v}"; shift ;;
	--domain) DOMAIN="$2"; shift ;;
	--proxy) PROXY="$2"; shift ;;
	--email) EMAIL="$2"; shift ;;
	--no-tls) DO_TLS=0 ;;
	--no-firewall) DO_FIREWALL=0 ;;
	--no-voice) DO_VOICE=0 ;;
	-y | --yes) ASSUME_YES=1 ;;
	-h | --help) usage; exit 0 ;;
	*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
	esac
	shift
done

# --- output and prompts ------------------------------------------------------

if [ -t 1 ]; then
	B=$'\033[1m' G=$'\033[32m' Y=$'\033[33m' R=$'\033[31m' N=$'\033[0m'
else
	B="" G="" Y="" R="" N=""
fi
step() { printf '\n%s==> %s%s\n' "$B" "$1" "$N"; }
info() { printf '    %s\n' "$1"; }
ok() { printf '    %s✓%s %s\n' "$G" "$N" "$1"; }
warn() { printf '    %s!%s %s\n' "$Y" "$N" "$1" >&2; }
die() { printf '%sError:%s %s\n' "$R" "$N" "$1" >&2; exit 1; }

# Prompts read the terminal, so they work under "curl ... | sudo bash".
can_ask() { [ "$ASSUME_YES" = 0 ] && { true </dev/tty; } 2>/dev/null; }

# ask VAR "Question" "default"
ask() {
	local __var=$1 __q=$2 __def=${3:-} __ans=""
	if can_ask; then
		if [ -n "$__def" ]; then
			read -r -p "    $__q [$__def]: " __ans </dev/tty || true
		else
			read -r -p "    $__q: " __ans </dev/tty || true
		fi
	fi
	printf -v "$__var" '%s' "${__ans:-$__def}"
}

# confirm "Question" y|n
confirm() {
	local def=$2 ans=""
	if can_ask; then
		local hint="[y/N]"
		[ "$def" = y ] && hint="[Y/n]"
		read -r -p "    $1 $hint " ans </dev/tty || true
	fi
	ans=${ans:-$def}
	case "$ans" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

have() { command -v "$1" >/dev/null 2>&1; }
active() { have systemctl && systemctl is-active --quiet "$1" 2>/dev/null; }

# --- checks ------------------------------------------------------------------

[ "$(uname -s)" = Linux ] || die "this script is for Linux. On macOS use Homebrew; on Windows see the README."
[ "$(id -u)" = 0 ] || die "run it as root, e.g. with sudo."
have curl || die "curl is required."
have tar || die "tar is required."

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv7l | armv7*) ARCH=armv7 ;;
*) die "unsupported CPU: $(uname -m). Build from source instead." ;;
esac

PKG=""
for p in apt-get dnf yum zypper pacman apk; do
	if have "$p"; then PKG=$p; break; fi
done

pkg_install() {
	case "$PKG" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null ;;
	dnf | yum) "$PKG" install -y -q "$@" >/dev/null ;;
	zypper) zypper --non-interactive --quiet install "$@" >/dev/null ;;
	pacman) pacman -S --noconfirm --needed "$@" >/dev/null ;;
	apk) apk add --quiet "$@" >/dev/null ;;
	*) return 1 ;;
	esac
}

APT_UPDATED=0
pkg_refresh() {
	if [ "$PKG" = apt-get ] && [ "$APT_UPDATED" = 0 ]; then
		apt-get update -qq >/dev/null || true
		APT_UPDATED=1
	fi
}

# --- 1. binary ---------------------------------------------------------------

step "Installing whatomate"
if [ -z "$VERSION" ]; then
	latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		die "could not reach GitHub to find the latest release."
	VERSION=${latest##*/v}
	[ -n "$VERSION" ] && [ "$VERSION" != "$latest" ] || die "could not find the latest release."
fi
archive="whatomate_${VERSION}_linux_${ARCH}.tar.gz"
base="https://github.com/$REPO/releases/download/v${VERSION}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "Downloading $archive"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "download failed: $base/$archive"
if curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" && have sha256sum; then
	(cd "$tmp" && grep " ${archive}\$" checksums.txt | sha256sum -c --quiet -) ||
		die "checksum mismatch for $archive."
	ok "Checksum verified"
else
	warn "Could not verify the checksum."
fi
tar -xzf "$tmp/$archive" -C "$tmp" whatomate

upgrade=0
[ -x "$BIN" ] && upgrade=1
was_running=0
if active whatomate; then
	was_running=1
	systemctl stop whatomate
fi
install -m 755 "$tmp/whatomate" "$BIN"
ok "Installed $("$BIN" version 2>/dev/null | head -1 || echo "$VERSION") to $BIN"

# --- 2. voice tools ----------------------------------------------------------

if [ "$DO_VOICE" = 1 ]; then
	step "Voice tools (WhatsApp calling and spoken IVR greetings)"
	missing=()
	for t in ffmpeg opusenc espeak-ng; do have "$t" || missing+=("$t"); done
	if [ ${#missing[@]} = 0 ]; then
		ok "ffmpeg, opus-tools and espeak-ng are installed"
	elif [ -z "$PKG" ]; then
		warn "No known package manager. Install ffmpeg, opus-tools and espeak-ng yourself."
	elif confirm "Install ffmpeg, opus-tools and espeak-ng?" y; then
		pkg_refresh
		for p in ffmpeg opus-tools espeak-ng; do
			if pkg_install "$p"; then ok "$p"; else warn "Could not install $p (on RHEL/Fedora, ffmpeg needs RPM Fusion)."; fi
		done
	fi
fi

# --- 3. service user and folders ---------------------------------------------

step "Service account and folders"
if ! id "$SVC_USER" >/dev/null 2>&1; then
	nologin=$(command -v nologin || echo /bin/false)
	useradd --system --home-dir "$DATA_DIR" --no-create-home --shell "$nologin" "$SVC_USER"
	ok "Created user $SVC_USER"
fi
for d in "$CONFIG_DIR" "$DATA_DIR" "$FILES_DIR" "$FILES_DIR/uploads" "$FILES_DIR/audio"; do
	mkdir -p "$d"
	chown "$SVC_USER:$SVC_USER" "$d"
	chmod 750 "$d"
done
ok "Config: $CONFIG_DIR   Data: $DATA_DIR   Uploads and audio: $FILES_DIR"

# --- 4. public IP for calls --------------------------------------------------

PUBLIC_IP=$(curl -fsS -4 --max-time 5 https://api.ipify.org 2>/dev/null || true)
CALL_IP=""
if [ -n "$PUBLIC_IP" ] && have ip && ! ip -o addr show | grep -qwF "$PUBLIC_IP"; then
	step "Public IP"
	info "This server reaches the internet as $PUBLIC_IP, which is not on any of its network interfaces"
	info "(typical for AWS, GCP and Azure). Calls need this address to connect."
	confirm "Use $PUBLIC_IP for call media?" y && CALL_IP=$PUBLIC_IP
fi

# --- 5. systemd service ------------------------------------------------------

step "Service"
if ! have systemctl || [ ! -d /run/systemd/system ]; then
	warn "systemd is not running here. Start whatomate yourself: sudo -u $SVC_USER $BIN server"
	SYSTEMD=0
else
	SYSTEMD=1
	if [ -f "$UNIT" ] && [ "$upgrade" = 1 ]; then
		info "Keeping your existing $UNIT"
	else
		{
			cat <<EOF
[Unit]
Description=Whatomate
After=network-online.target
Wants=network-online.target

[Service]
User=$SVC_USER
Group=$SVC_USER
WorkingDirectory=$DATA_DIR
ExecStart=$BIN server
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
EOF
			[ -n "$CALL_IP" ] && echo "Environment=WHATOMATE_CALLING__PUBLIC_IP=$CALL_IP"
			cat <<EOF

[Install]
WantedBy=multi-user.target
EOF
		} >"$UNIT"
		ok "Wrote $UNIT"
	fi
	systemctl daemon-reload
	systemctl enable whatomate >/dev/null 2>&1
fi

# --- 6. reverse proxy --------------------------------------------------------

detect_proxies() {
	local found=()
	if have apache2ctl || have apache2 || [ -d /etc/apache2 ]; then found+=(apache)
	elif have httpd || [ -d /etc/httpd ]; then found+=(apache); fi
	{ have nginx || [ -d /etc/nginx ]; } && found+=(nginx)
	{ have caddy || [ -f /etc/caddy/Caddyfile ]; } && found+=(caddy)
	echo "${found[*]:-}"
}

apache_service() { if [ -d /etc/apache2 ]; then echo apache2; else echo httpd; fi; }
apachectl_cmd() { if have apache2ctl; then echo apache2ctl; else echo apachectl; fi; }

# apache_ws_native reports whether Apache supports "upgrade=websocket" (2.4.47+).
apache_ws_native() {
	local v
	v=$($(apachectl_cmd) -v 2>/dev/null | sed -n 's|.*Apache/\([0-9.]*\).*|\1|p' | head -1)
	[ -n "$v" ] || return 0
	[ "$(printf '%s\n2.4.47\n' "$v" | sort -V | head -1)" = 2.4.47 ]
}

write_apache() {
	local conf
	if [ -d /etc/webmin/virtual-server ]; then
		warn "Virtualmin manages the Apache sites here, so the script won't add one."
		info "In Virtualmin, open $DOMAIN → Web Configuration → Edit Directives (both the port 80 and 443 sites),"
		info "and add:"
		echo
		apache_directives | sed 's/^/        /'
		echo
		info "Enable the modules once: a2enmod proxy proxy_http proxy_wstunnel headers rewrite; systemctl restart apache2"
		return 1
	fi
	if [ -d /etc/apache2/sites-available ]; then
		conf=/etc/apache2/sites-available/whatomate.conf
		a2enmod -q proxy proxy_http proxy_wstunnel headers rewrite >/dev/null
	else
		conf=/etc/httpd/conf.d/whatomate.conf
	fi
	{
		echo "<VirtualHost *:80>"
		echo "    ServerName $DOMAIN"
		apache_directives | sed 's/^/    /'
		echo "</VirtualHost>"
	} >"$conf"
	[ -d /etc/apache2/sites-available ] && a2ensite -q whatomate >/dev/null
	$(apachectl_cmd) configtest >/dev/null 2>&1 || { $(apachectl_cmd) configtest; die "Apache rejected $conf."; }
	if active "$(apache_service)"; then
		systemctl reload "$(apache_service)"
	elif have systemctl; then
		systemctl enable --now "$(apache_service)" >/dev/null 2>&1 || true
	fi
	ok "Apache site for $DOMAIN: $conf"
}

apache_directives() {
	cat <<EOF
ProxyPreserveHost On
ProxyRequests Off
ProxyTimeout 3600
LimitRequestBody 0
RequestHeader set X-Forwarded-Proto expr=%{REQUEST_SCHEME}
EOF
	if apache_ws_native; then
		echo "ProxyPass / http://127.0.0.1:$APP_PORT/ upgrade=websocket"
	else
		cat <<EOF
RewriteEngine On
RewriteCond %{HTTP:Upgrade} =websocket [NC]
RewriteRule ^/(.*)\$ ws://127.0.0.1:$APP_PORT/\$1 [P,L]
ProxyPass / http://127.0.0.1:$APP_PORT/
EOF
	fi
	echo "ProxyPassReverse / http://127.0.0.1:$APP_PORT/"
}

write_nginx() {
	local conf link=""
	if [ -d /etc/nginx/sites-available ]; then
		conf=/etc/nginx/sites-available/whatomate.conf
		link=/etc/nginx/sites-enabled/whatomate.conf
	else
		conf=/etc/nginx/conf.d/whatomate.conf
	fi
	cat >"$conf" <<EOF
map \$http_upgrade \$whatomate_connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;

    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:$APP_PORT;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-Host \$host;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$whatomate_connection_upgrade;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
        proxy_request_buffering off;
    }
}
EOF
	[ -n "$link" ] && ln -sf "$conf" "$link"
	nginx -t >/dev/null 2>&1 || { nginx -t; die "nginx rejected $conf."; }
	if active nginx; then systemctl reload nginx; elif have systemctl; then systemctl enable --now nginx >/dev/null 2>&1 || true; fi
	ok "nginx site for $DOMAIN: $conf"
}

write_caddy() {
	local conf=/etc/caddy/Caddyfile
	mkdir -p /etc/caddy
	if [ -f "$conf" ] && grep -qE "^[[:space:]]*$DOMAIN([[:space:],{]|$)" "$conf"; then
		warn "$conf already has a site for $DOMAIN; leaving it as is."
	else
		[ -f "$conf" ] && cp -f "$conf" "$conf.bak.$(date +%Y%m%d%H%M%S)"
		cat >>"$conf" <<EOF

$DOMAIN {
    reverse_proxy 127.0.0.1:$APP_PORT
}
EOF
	fi
	caddy validate --config "$conf" --adapter caddyfile >/dev/null 2>&1 || { caddy validate --config "$conf" --adapter caddyfile; die "Caddy rejected $conf."; }
	if active caddy; then systemctl reload caddy; elif have systemctl; then systemctl enable --now caddy >/dev/null 2>&1 || true; fi
	ok "Caddy site for $DOMAIN (Caddy gets the HTTPS certificate itself)"
}

# SELinux (RHEL, Fedora) stops web servers from connecting to local ports.
allow_selinux_proxy() {
	if have getenforce && [ "$(getenforce 2>/dev/null)" != Disabled ] && have setsebool; then
		setsebool -P httpd_can_network_connect 1 && ok "SELinux: allowed the web server to reach whatomate"
	fi
	return 0
}

get_certificate() {
	local plugin=$1
	[ "$DO_TLS" = 0 ] && return 0
	confirm "Get a free Let's Encrypt HTTPS certificate for $DOMAIN now?" y || return 0
	if ! have certbot; then
		pkg_refresh
		case "$PKG" in
		apt-get) pkg_install certbot "python3-certbot-$plugin" ;;
		dnf | yum) pkg_install certbot "python3-certbot-$plugin" ;;
		*) false ;;
		esac || { warn "Could not install certbot. Install it and run: certbot --$plugin -d $DOMAIN"; return 0; }
	fi
	[ -n "$EMAIL" ] || ask EMAIL "Email for certificate expiry notices (optional)" ""
	local mail=(--register-unsafely-without-email)
	[ -n "$EMAIL" ] && mail=(-m "$EMAIL")
	if certbot "--$plugin" -d "$DOMAIN" --non-interactive --agree-tos --redirect "${mail[@]}"; then
		ok "HTTPS is on for $DOMAIN"
		HTTPS_OK=1
	else
		warn "certbot failed. Check that $DOMAIN points at this server and ports 80/443 are open, then run:"
		info "certbot --$plugin -d $DOMAIN"
	fi
}

HTTPS_OK=0
PROXY_DONE=0
step "Reverse proxy"
proxies=$(detect_proxies)
if [ "$PROXY" = none ]; then
	info "Skipped (--proxy none)"
else
	if [ -z "$PROXY" ]; then
		if [ -z "$proxies" ]; then
			info "No Apache, nginx or Caddy found."
			if [ -n "$PKG" ] && confirm "Install nginx as the reverse proxy?" n; then
				pkg_refresh
				pkg_install nginx || die "could not install nginx."
				PROXY=nginx
			fi
		else
			# Prefer the one that is running.
			for p in $proxies; do
				svc=$p
				[ "$p" = apache ] && svc=$(apache_service)
				if active "$svc"; then PROXY=$p; break; fi
			done
			# shellcheck disable=SC2086 # split the list on purpose
			set -- $proxies
			[ -n "$PROXY" ] || PROXY=$1
			if [ $# -gt 1 ]; then
				info "Found: $proxies"
				ask PROXY "Which one should serve whatomate?" "$PROXY"
			else
				info "Found: $PROXY"
			fi
		fi
	fi
	case "${PROXY:-}" in
	"") ;;
	apache | nginx | caddy)
		[ -n "$DOMAIN" ] || ask DOMAIN "Domain for whatomate (e.g. chat.example.com; empty to skip)" ""
		DOMAIN=${DOMAIN#http://}; DOMAIN=${DOMAIN#https://}; DOMAIN=${DOMAIN%%/*}
		if [ -z "$DOMAIN" ]; then
			info "No domain; skipping the reverse proxy."
		elif ! [[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]]; then
			die "\"$DOMAIN\" is not a valid domain name."
		else
			resolved=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}' || true)
			if [ -z "$resolved" ]; then
				warn "$DOMAIN does not resolve yet. Point its DNS A record at ${PUBLIC_IP:-this server}."
			elif [ -n "$PUBLIC_IP" ] && [ "$resolved" != "$PUBLIC_IP" ]; then
				warn "$DOMAIN points at $resolved, but this server is $PUBLIC_IP."
			fi
			allow_selinux_proxy
			case "$PROXY" in
			apache) if write_apache; then PROXY_DONE=1; get_certificate apache; fi ;;
			nginx) if write_nginx; then PROXY_DONE=1; get_certificate nginx; fi ;;
			caddy) if write_caddy; then PROXY_DONE=1; HTTPS_OK=1; fi ;;
			esac
		fi
		;;
	*) die "unknown proxy \"$PROXY\" (use apache, nginx, caddy or none)." ;;
	esac
fi

# --- 7. firewall -------------------------------------------------------------

open_ports() {
	local tcp=("$@") fw=""
	if have ufw && ufw status 2>/dev/null | grep -q "Status: active"; then fw=ufw
	elif have firewall-cmd && firewall-cmd --state >/dev/null 2>&1; then fw=firewalld
	elif have iptables; then fw=iptables
	fi
	case "$fw" in
	ufw)
		for p in "${tcp[@]}"; do ufw allow "$p/tcp" >/dev/null; done
		ufw allow "$UDP_MIN:$UDP_MAX/udp" >/dev/null
		;;
	firewalld)
		for p in "${tcp[@]}"; do firewall-cmd --permanent --add-port="$p/tcp" >/dev/null; done
		firewall-cmd --permanent --add-port="$UDP_MIN-$UDP_MAX/udp" >/dev/null
		firewall-cmd --reload >/dev/null
		;;
	iptables)
		local ipt
		for ipt in iptables ip6tables; do
			have "$ipt" || continue
			for p in "${tcp[@]}"; do
				$ipt -C INPUT -p tcp --dport "$p" -j ACCEPT 2>/dev/null || $ipt -I INPUT -p tcp --dport "$p" -j ACCEPT
			done
			$ipt -C INPUT -p udp --dport "$UDP_MIN:$UDP_MAX" -j ACCEPT 2>/dev/null ||
				$ipt -I INPUT -p udp --dport "$UDP_MIN:$UDP_MAX" -j ACCEPT
		done
		if have netfilter-persistent; then
			netfilter-persistent save >/dev/null 2>&1 && ok "Saved the iptables rules"
		elif have service && [ -f /etc/sysconfig/iptables ]; then
			service iptables save >/dev/null 2>&1 && ok "Saved the iptables rules"
		else
			warn "iptables rules are lost on reboot. To keep them: apt install iptables-persistent (Debian/Ubuntu)."
		fi
		;;
	*)
		warn "No ufw, firewalld or iptables found; nothing to open."
		return 0
		;;
	esac
	ok "Opened with $fw: TCP ${tcp[*]}, UDP $UDP_MIN-$UDP_MAX (calls)"
}

if [ "$DO_FIREWALL" = 1 ]; then
	step "Firewall"
	ports=()
	if [ "$PROXY_DONE" = 1 ]; then
		ports+=(80 443)
		confirm "Also open port $APP_PORT directly? (not needed behind the proxy)" n && ports+=("$APP_PORT")
	else
		ports+=("$APP_PORT")
	fi
	open_ports "${ports[@]}"
	info "On a cloud server, also allow these ports in the provider's firewall or security group."
fi

# --- 8. start ----------------------------------------------------------------

step "Starting whatomate"
config_exists=0
for f in "$CONFIG_DIR/config.toml" "$DATA_DIR/.config/whatomate/config.toml"; do
	[ -f "$f" ] && config_exists=1
done

if [ "$SYSTEMD" = 1 ]; then
	since=$(date '+%Y-%m-%d %H:%M:%S')
	systemctl restart whatomate
	if [ "$config_exists" = 1 ]; then
		sleep 2
		if active whatomate; then ok "whatomate is running"; else warn "whatomate did not start; see: journalctl -u whatomate -e"; fi
	else
		link=""
		for _ in $(seq 1 30); do
			link=$(journalctl -u whatomate --since "$since" -o cat 2>/dev/null | grep -o 'http://[^ "]*#token=[A-Za-z0-9_-]*' | head -1 || true)
			[ -n "$link" ] && break
			sleep 1
		done
		if [ -z "$link" ]; then
			warn "The setup link didn't show up yet. Find it with: journalctl -u whatomate | grep token="
		else
			token=${link##*#token=}
			echo
			printf '    %sOpen the setup wizard to finish:%s\n' "$B" "$N"
			if [ "$PROXY_DONE" = 1 ]; then
				scheme=http
				[ "$HTTPS_OK" = 1 ] && scheme=https
				printf '      %s://%s/#token=%s\n' "$scheme" "$DOMAIN" "$token"
			fi
			[ -n "$PUBLIC_IP" ] && printf '      http://%s:%s/#token=%s\n' "$PUBLIC_IP" "$APP_PORT" "$token"
			printf '      %s\n' "$link"
			info "The link works once; it's also in: journalctl -u whatomate"
		fi
	fi
fi

echo
if [ "$upgrade" = 1 ]; then
	ok "whatomate $VERSION is installed (upgrade)."
	[ "$was_running" = 1 ] || [ "$SYSTEMD" = 0 ] || info "It is now running as a service."
else
	ok "whatomate $VERSION is installed."
fi
info "Logs:    journalctl -u whatomate -f"
info "Config:  whatomate config path   (as $SVC_USER: sudo -u $SVC_USER whatomate config path)"
info "Upgrade: run this script again."
