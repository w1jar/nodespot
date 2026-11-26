#!/bin/sh
# prerm for nodespot. POSIX sh; safe to run repeatedly.
set -e

DATADIR=/var/lib/nodespot
FW_MARKER="$DATADIR/.firewalld-rule-added"

# The setup hotspot: stop it and delete its profile and firewall zone, so a
# removed package leaves no open access point behind.
remove_hotspot() {
	command -v nmcli >/dev/null 2>&1 || return 0
	nmcli connection down id nodespot-hotspot >/dev/null 2>&1 || true
	nmcli connection delete id nodespot-hotspot >/dev/null 2>&1 || true
	if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
		if firewall-cmd --permanent --get-zones 2>/dev/null | tr ' ' '\n' | grep -qx nodespot-hotspot; then
			firewall-cmd --permanent --delete-zone=nodespot-hotspot >/dev/null 2>&1 || true
			firewall-cmd --reload >/dev/null 2>&1 || true
		fi
	fi
}

remove_firewalld_rule() {
	[ -e "$FW_MARKER" ] || return 0
	if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd --permanent --remove-service=nodespot >/dev/null 2>&1 || true
		firewall-cmd --reload >/dev/null 2>&1 || true
		echo "nodespot: removed the firewalld rule for the 'nodespot' service."
	elif command -v firewall-offline-cmd >/dev/null 2>&1; then
		firewall-offline-cmd --remove-service=nodespot >/dev/null 2>&1 || true
	fi
	rm -f "$FW_MARKER"
}

case "$1" in
remove | deconfigure)
	if [ -d /run/systemd/system ]; then
		systemctl stop nodespot-node.service >/dev/null 2>&1 || true
		systemctl disable nodespot-node.service >/dev/null 2>&1 || true
		systemctl stop nodespot-dv.service >/dev/null 2>&1 || true
		systemctl disable nodespot-dv.service >/dev/null 2>&1 || true
		systemctl stop nodespot-wifi-watchdog.timer nodespot-wifi-watchdog.service >/dev/null 2>&1 || true
		systemctl disable nodespot-wifi-watchdog.timer >/dev/null 2>&1 || true
		systemctl stop nodespot.service nodespot-helper.service nodespot-helper.socket >/dev/null 2>&1 || true
		systemctl disable nodespot.service nodespot-helper.socket >/dev/null 2>&1 || true
	fi
	remove_hotspot
	remove_firewalld_rule
	;;
upgrade | failed-upgrade) ;;
*)
	echo "prerm called with unknown argument '$1'" >&2
	exit 1
	;;
esac

exit 0
