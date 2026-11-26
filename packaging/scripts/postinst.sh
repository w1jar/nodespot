#!/bin/sh
# postinst for nodespot. POSIX sh; safe to run repeatedly.
set -e

DATADIR=/var/lib/nodespot
FW_MARKER="$DATADIR/.firewalld-rule-added"
FW_SUM="$DATADIR/.firewalld-service-sum"
FW_SERVICE_FILE=/usr/lib/firewalld/services/nodespot.xml
NODE_MARKER="$DATADIR/.node-enabled"

# Timing notes (Raspberry Pi 3): every firewall-cmd call starts Python
# (~1 s), a firewalld reload takes several seconds, runuser/su start a PAM
# session. An upgrade therefore skips everything that is already in place and
# restarts nodespot.service LAST, so the web interface is back as early as
# possible and the upgrade's own journal stays short.
FIRST_INSTALL=

have_systemd() { [ -d /run/systemd/system ]; }

configure_user() {
	if ! getent passwd nodespot >/dev/null 2>&1; then
		if getent group nodespot >/dev/null 2>&1; then
			adduser --system --ingroup nodespot --home "$DATADIR" --no-create-home \
				--gecos "nodespot web control panel" --shell /usr/sbin/nologin nodespot
		else
			adduser --system --group --home "$DATADIR" --no-create-home \
				--gecos "nodespot web control panel" --shell /usr/sbin/nologin nodespot
		fi
	fi
	install -d -m 0750 -o nodespot -g nodespot "$DATADIR"
}

# The native node runs as its own user, so it never gets access to
# /var/lib/nodespot. It owns /etc/nodespot/node.json (0600: it holds network
# passwords) and gets the SHARI's devices through 60-nodespot-node.rules. The
# nodespot user joins its group to reach the control socket (mode 0660,
# /run/nodespot-node/control.sock).
configure_node_user() {
	if ! getent passwd nodespot-node >/dev/null 2>&1; then
		if getent group nodespot-node >/dev/null 2>&1; then
			adduser --system --ingroup nodespot-node --home /nonexistent --no-create-home \
				--gecos "nodespot native node" --shell /usr/sbin/nologin nodespot-node
		else
			adduser --system --group --home /nonexistent --no-create-home \
				--gecos "nodespot native node" --shell /usr/sbin/nologin nodespot-node
		fi
	fi
	if getent group audio >/dev/null 2>&1 && ! id -nG nodespot-node | tr ' ' '\n' | grep -qx audio; then
		adduser nodespot-node audio >/dev/null
	fi
	if ! id -nG nodespot | tr ' ' '\n' | grep -qx nodespot-node; then
		adduser nodespot nodespot-node >/dev/null
	fi
	# The node writes its config next to nodespot.conf (atomically, so it needs
	# the directory); everyone else keeps read access.
	install -d -m 0775 -o root -g nodespot-node /etc/nodespot
	for f in /etc/nodespot/node.json /etc/nodespot/sa818.json; do
		[ -e "$f" ] || continue
		chown nodespot-node:nodespot-node "$f"
		chmod 0600 "$f"
	done
	if command -v udevadm >/dev/null 2>&1; then
		udevadm control --reload >/dev/null 2>&1 || true
		udevadm trigger --action=change --subsystem-match=hidraw --subsystem-match=tty >/dev/null 2>&1 || true
	fi
}

# Everything the restart of nodespot.service depends on. daemon-reload is fast
# and needed in case unit files changed.
prepare_services() {
	have_systemd || return 0
	systemctl daemon-reload || true
	if [ -n "$FIRST_INSTALL" ]; then
		systemctl enable nodespot-helper.socket nodespot.service >/dev/null 2>&1 || true
	fi
	# The setup hotspot watchdog (it does nothing without NetworkManager and
	# a WiFi device). Enabled on first install only, so an admin who disabled
	# the timer keeps it off; started on every configure to run the new code.
	if [ -n "$FIRST_INSTALL" ]; then
		systemctl enable nodespot-wifi-watchdog.timer >/dev/null 2>&1 || true
	fi
	if systemctl is-enabled nodespot-wifi-watchdog.timer >/dev/null 2>&1; then
		systemctl restart nodespot-wifi-watchdog.timer >/dev/null 2>&1 || true
	fi
	# 3.0 replaced the digital voice engine with the native node: stop the
	# old engine if it is still running from before the upgrade.
	if systemctl is-active nodespot-dv.service >/dev/null 2>&1 || systemctl is-enabled nodespot-dv.service >/dev/null 2>&1; then
		systemctl disable --now nodespot-dv.service >/dev/null 2>&1 || true
	fi
	# The native node is enabled once (first install, or the upgrade that
	# brings it), so an admin who disabled it keeps it off; an enabled node
	# runs the new binary after an upgrade.
	if [ ! -e "$NODE_MARKER" ]; then
		systemctl enable nodespot-node.service >/dev/null 2>&1 || true
		touch "$NODE_MARKER" 2>/dev/null || true
	fi
	if systemctl is-enabled nodespot-node.service >/dev/null 2>&1; then
		systemctl restart nodespot-node.service >/dev/null 2>&1 || echo "nodespot: warning: could not start nodespot-node.service (see: journalctl -u nodespot-node)"
	fi
	# Restart (not just start) so an upgrade runs the new helper binary.
	systemctl stop nodespot-helper.service >/dev/null 2>&1 || true
	systemctl restart nodespot-helper.socket || echo "nodespot: warning: could not start nodespot-helper.socket"
}

# The LAST step: the web interface is down only for the restart itself.
restart_app() {
	have_systemd || return 0
	systemctl restart nodespot.service || echo "nodespot: warning: could not start nodespot.service (see: journalctl -u nodespot)"
}

fw_service_sum() {
	cksum <"$FW_SERVICE_FILE" 2>/dev/null | awk '{print $1 ":" $2}'
}

# conf_port: TCP port of "listen" in the [server] section of nodespot.conf
# (":8080" or "addr:port"); 8080 when unset or invalid.
conf_port() {
	l=$(awk '
		/^[ \t]*\[/ { sec = ($0 ~ /^[ \t]*\[server\][ \t]*$/); next }
		sec && /^[ \t]*[;#]/ { next }
		sec { i = index($0, "="); if (i == 0) next
			k = substr($0, 1, i - 1); gsub(/[ \t]/, "", k)
			if (k == "listen") { val = substr($0, i + 1); sub(/[ \t]*[;#].*$/, "", val); gsub(/^[ \t]+|[ \t]+$/, "", val); out = val } }
		END { print out }' "${NODESPOT_CONF:-/etc/nodespot/nodespot.conf}" 2>/dev/null || true)
	p=${l##*:}
	case "$p" in
	'' | *[!0-9]* | 0*) p=8080 ;;
	esac
	[ "${#p}" -le 5 ] && [ "$p" -le 65535 ] || p=8080
	printf '%s\n' "$p"
}

# Port 8080 uses the packaged firewalld service "nodespot"; any other port is
# opened directly (--add-port).
configure_firewalld() {
	command -v firewall-cmd >/dev/null 2>&1 || return 0
	port=$(conf_port)
	if [ "$port" = 8080 ]; then
		fw_query=--query-service=nodespot fw_add=--add-service=nodespot fw_what="service 'nodespot'"
	else
		fw_query=--query-port=$port/tcp fw_add=--add-port=$port/tcp fw_what="port"
	fi
	sum=$(fw_service_sum)
	# Fast path on upgrade (1 firewall-cmd call, no reload): the rule is
	# already active (port 8080: and the service definition is unchanged).
	if [ -z "$FIRST_INSTALL" ] && firewall-cmd "$fw_query" >/dev/null 2>&1; then
		if [ "$port" != 8080 ]; then
			return 0
		elif [ -n "$sum" ] && [ "$sum" = "$(cat "$FW_SUM" 2>/dev/null)" ]; then
			return 0
		fi
	fi
	firewall-cmd --state >/dev/null 2>&1 || return 0
	# Reload so firewalld knows the service definition shipped by this package.
	firewall-cmd --reload >/dev/null 2>&1 || true
	if firewall-cmd --permanent "$fw_query" >/dev/null 2>&1; then
		[ -z "$sum" ] || echo "$sum" >"$FW_SUM" 2>/dev/null || true
		return 0
	fi
	if firewall-cmd --permanent "$fw_add" >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1; then
		touch "$FW_MARKER"
		[ -z "$sum" ] || echo "$sum" >"$FW_SUM" 2>/dev/null || true
		echo "nodespot: firewalld is running: opened TCP $port ($fw_what, permanent)."
	else
		echo "nodespot: warning: firewalld is running but opening TCP $port failed;"
		echo "nodespot:   run: firewall-cmd --permanent $fw_add && firewall-cmd --reload"
	fi
}

first_install_message() {
	ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	[ -n "$ip" ] || ip=$(hostname 2>/dev/null || echo localhost)
	code=
	n=0
	while [ "$n" -lt 5 ] && [ ! -s "$DATADIR/setup-code" ]; do
		sleep 1
		n=$((n + 1))
	done
	code=$(head -n 1 "$DATADIR/setup-code" 2>/dev/null || true)
	port=$(conf_port)
	if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
		G=$(printf '\033[1;32m') Y=$(printf '\033[1;33m') R=$(printf '\033[0m')
	else
		G='' Y='' R=''
	fi
	printf '\n%snodespot: installed.%s\n' "$G" "$R"
	if [ -n "$code" ]; then
		printf 'nodespot: open the one-time setup link to create the administrator or restore a backup:\n'
		printf '    %shttp://%s:%s/#/setup?code=%s%s\n' "$Y" "$ip" "$port" "$code" "$R"
		echo "nodespot: show the code again:   sudo nodespot admin setup-code"
	else
		echo "nodespot: open http://$ip:$port and get the setup code with:   sudo nodespot admin setup-code"
	fi
	echo "nodespot: or create the admin in the terminal:   sudo nodespot admin create"
	echo
}

case "$1" in
configure)
	[ -n "$2" ] || FIRST_INSTALL=1
	configure_user
	configure_node_user
	prepare_services
	configure_firewalld
	restart_app
	# install.sh prints the setup link itself once everything (the port) is
	# done, so it sets NODESPOT_INSTALLER=1 to keep this one from showing early.
	if [ -z "$2" ] && [ -z "${NODESPOT_INSTALLER:-}" ]; then
		first_install_message
	fi
	;;
abort-upgrade | abort-remove | abort-deconfigure) ;;
*)
	echo "postinst called with unknown argument '$1'" >&2
	exit 1
	;;
esac

exit 0
