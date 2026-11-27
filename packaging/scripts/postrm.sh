#!/bin/sh
# postrm for nodespot. POSIX sh; safe to run repeatedly.
#
# This script must never fail a remove or a purge: every step logs and goes on.
# The NODESPOT_POSTRM_* variables exist for the unit tests (a fake systemctl and
# a temporary root directory); nothing sets them on a real system.

ROOT=${NODESPOT_POSTRM_ROOT:-}
RECORD=$ROOT/var/lib/nodespot/disabled-units
DROPIN_DIR=$ROOT/etc/systemd/system/nodespot.service.d
SYSTEMD_RUN_DIR=${NODESPOT_POSTRM_SYSTEMD_DIR:-/run/systemd/system}

have_systemd() { [ -d "$SYSTEMD_RUN_DIR" ]; }

daemon_reload() {
	have_systemd || return 0
	systemctl daemon-reload || echo "nodespot: systemctl daemon-reload failed (ignored)" >&2
	return 0
}

# The units the services switch may have turned off (the allowlist of
# internal/helper/sys_svc.go). The record sits in a directory the nodespot user
# owns, so nothing else in it is trusted.
valid_unit() {
	case "$1" in
	cockpit.socket | cockpit.service | apache2 | allmon3) return 0 ;;
	esac
	return 1
}

# Remove nodespot's own port 80 drop-in: only that file, then the directory if it
# is empty.
remove_dropin() {
	rm -f "$DROPIN_DIR/port80.conf" || echo "nodespot: cannot remove $DROPIN_DIR/port80.conf (ignored)" >&2
	rmdir "$DROPIN_DIR" 2>/dev/null || true
	return 0
}

# Re-enable exactly the units nodespot turned off, then delete the record. A unit
# that nodespot did not disable, or that the operator disabled by hand, is not in
# the record and stays as it is.
restore_units() {
	[ -f "$RECORD" ] && [ ! -L "$RECORD" ] || return 0
	if have_systemd; then
		seen=" "
		while IFS= read -r line || [ -n "$line" ]; do
			unit=$(printf '%s' "$line" | tr -d ' \t\r')
			valid_unit "$unit" || continue
			case "$seen" in *" $unit "*) continue ;; esac
			seen="$seen$unit "
			echo "nodespot: turning $unit back on (nodespot had turned it off)"
			systemctl enable --now "$unit" || echo "nodespot: could not enable $unit (ignored)" >&2
		done <"$RECORD"
	fi
	rm -f "$RECORD" || echo "nodespot: cannot remove $RECORD (ignored)" >&2
	return 0
}

case "$1" in
remove)
	daemon_reload
	;;
purge)
	# nodespot's own port 80 drop-in goes first and systemd reloads, so port 80
	# is free when Apache comes back (nodespot.service was stopped by prerm).
	remove_dropin
	daemon_reload
	# The record lives in /var/lib/nodespot: read it before that goes away.
	restore_units
	rm -rf "$ROOT/var/lib/nodespot" "$ROOT/run/nodespot"
	# The digital voice engine: its config (may hold network passwords), its
	# runtime directory and its system user.
	rm -rf "$ROOT/var/lib/nodespot-dv" "$ROOT/run/nodespot-dv"
	# The root-only state of the helper: the config history (git), the
	# digital modes install and bridge-node records, pending restored settings
	# and the ini history of the previous stack (both hold passwords), and the
	# port 80 move state. The directories of 1.x installs are removed too (the
	# helper renames them on start; this covers a node purged before that).
	# The previous stack's own files are not touched.
	rm -rf "$ROOT/var/lib/nodespot-config-history" "$ROOT/var/lib/nodespot-dvstate" "$ROOT/var/lib/nodespot-dvstate-history" "$ROOT"/var/lib/nodespot-dvswitch* "$ROOT/var/lib/nodespot-webport"
	# The hotspot setting (WiFi networks saved in NetworkManager stay).
	rm -rf "$ROOT/var/lib/nodespot-wifi"
	# The native node: its config and the SA818 state (node.json holds
	# network passwords), its runtime directory and its system user.
	rm -f "$ROOT/etc/nodespot/node.json" "$ROOT/etc/nodespot/sa818.json" "$ROOT"/etc/nodespot/.node-*.json
	rm -rf "$ROOT/run/nodespot-node"
	rmdir "$ROOT/etc/nodespot" 2>/dev/null || true
	if getent passwd nodespot-node >/dev/null 2>&1; then
		deluser --system nodespot-node >/dev/null 2>&1 || true
	fi
	if getent group nodespot-node >/dev/null 2>&1; then
		delgroup --system nodespot-node >/dev/null 2>&1 || true
	fi
	if getent passwd nodespot-dv >/dev/null 2>&1; then
		deluser --system nodespot-dv >/dev/null 2>&1 || true
	fi
	if getent group nodespot-dv >/dev/null 2>&1; then
		delgroup --system nodespot-dv >/dev/null 2>&1 || true
	fi
	if getent passwd nodespot >/dev/null 2>&1; then
		deluser --system nodespot >/dev/null 2>&1 || true
	fi
	if getent group nodespot >/dev/null 2>&1; then
		delgroup --system nodespot >/dev/null 2>&1 || true
	fi
	;;
upgrade | failed-upgrade | abort-install | abort-upgrade | disappear) ;;
*)
	echo "postrm called with unknown argument '$1'" >&2
	exit 1
	;;
esac

exit 0
