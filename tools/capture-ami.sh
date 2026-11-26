#!/bin/bash
# Capture read-only app_rpt status replies and events over AMI, as test data
# for nodespot. Run as root on the node. Sends no commands that change anything.
# The AMI login is read from manager.conf and is not written to the output.
set -euo pipefail
OUT=${1:-nodespot-ami-capture.txt}
LISTEN=${LISTEN:-60}
ETC=${ASTERISK_ETC:-/etc/asterisk}
CONF=$ETC/manager.conf

read -r AMIUSER AMISECRET < <(awk -F= '
  /^[ \t]*\[/ { sec=$0; sub(/\].*/,"",sec); sub(/^[ \t]*\[/,"",sec); next }
  sec != "" && sec != "general" && $1 ~ /^[ \t]*secret[ \t]*$/ {
    s=$2; sub(/;.*/,"",s); gsub(/[ \t]/,"",s); print sec, s; exit }' "$CONF")
[ -n "${AMISECRET:-}" ] || { echo "No AMI user found in $CONF" >&2; exit 1; }
NODES=$(grep -oE '^\[[0-9]+\]' $ETC/rpt.conf | tr -d '[]' | tr '\n' ' ')
echo "AMI user: $AMIUSER   nodes: $NODES"

exec 3<>/dev/tcp/127.0.0.1/${AMIPORT:-5038}
timeout "$LISTEN" cat <&3 > "$OUT" &
READER=$!
send() { printf '%s\r\n' "$@" "" >&3; sleep 0.3; }

send "Action: Login" "Username: $AMIUSER" "Secret: $AMISECRET" "Events: call" "ActionID: login"
unset AMISECRET
send "Action: RptLocalNodes" "ActionID: localnodes"
send "Action: RptStatus" "Command: RptStat" "ActionID: rptstat"
for n in $NODES; do
  for c in XStat SawStat NodeStat; do
    send "Action: RptStatus" "Command: $c" "Node: $n" "ActionID: $c-$n"
  done
done
send "Action: Command" "Command: core show uptime seconds" "ActionID: uptime"
send "Action: Command" "Command: core show version" "ActionID: version"
send "Action: Command" "Command: rpt localnodes" "ActionID: cli-localnodes"

echo "Recording events for ${LISTEN}s. Key up, connect or disconnect a node now..."
wait "$READER" || true
printf 'Action: Logoff\r\n\r\n' >&3 2>/dev/null || true
exec 3>&-
echo "Done: $OUT ($(wc -l < "$OUT") lines)"
