#!/bin/bash
# Build/refresh the apt repository: the gh-pages branch of the code repository,
# served by GitHub Pages (packaging/apt-checkout.sh prepares the checkout).
#
#   packaging/publish-apt.sh DEBS_DIR REPO_DIR
#       add the new nodespot_*.deb files to the beta pool (what a vX.Y.Z tag does)
#   packaging/publish-apt.sh --promote VERSION REPO_DIR
#       move the already published debs of VERSION from the beta pool to the
#       stable pool (no rebuild: the tested package becomes the release)
#   packaging/publish-apt.sh --remove GLOB [--apply] REPO_DIR
#       delete beta-pool versions matching GLOB (a bash pattern on the Debian
#       version, for example '2.0.0~beta*'); a dry run unless --apply is given
#
#   DEBS_DIR  directory containing the new nodespot_*.deb files
#   REPO_DIR  checkout of the gh-pages branch (may be empty/new on the very first
#             release); updated in place, the caller commits and pushes
#
# Environment:
#   GPG_KEY_ID    key to sign with (default: first secret key in the keyring)
#   GNUPGHOME     keyring directory (default: gpg's default)
#   APT_URL       public base URL (default https://w1jar.github.io/nodespot)
#   KEEP          versions retained per architecture and pool directory (default 5)
#   TAG           release tag (vX.Y.Z) for the release notes
#   CODE_DIR      checkout of the code repo, for the tag annotation and CHANGELOG.md
#                 (default: the repo containing this script)
#   NOTES_FILE    explicit release notes file (overrides TAG/CHANGELOG lookup)
#   REQUIRE_VERSION  with --remove: abort unless this version is still in the
#                 pool afterwards (a guard against emptying the repository)
#
# Two suites live in one repository:
#   beta    indexes all of pool/ (new builds in pool/beta, plus everything
#           promoted to pool/main)
#   stable  indexes pool/main only (promoted releases)
# Every version tag builds into pool/beta. Promotion moves the same files to
# pool/main, so a package is in one pool at a time and the beta suite still
# offers it. latest-beta.json is the newest version in the whole pool,
# latest.json the newest in pool/main.
# install.sh is only refreshed when the version being published is the newest in
# the whole pool (a backport must not revert it).
#
# Requires apt-ftparchive (apt-utils), gpg, dpkg-deb and python3.
set -euo pipefail

usage() { awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 2; }

MODE=add
PROMOTE_VERSION=""
REMOVE_GLOB=""
APPLY=0
args=()
while [ $# -gt 0 ]; do
	case "$1" in
	--promote) [ $# -ge 2 ] || usage; MODE=promote; PROMOTE_VERSION=${2#v}; shift 2 ;;
	--remove) [ $# -ge 2 ] || usage; MODE=remove; REMOVE_GLOB=$2; shift 2 ;;
	--apply) APPLY=1; shift ;;
	-h | --help) usage ;;
	*) args+=("$1"); shift ;;
	esac
done
case "$MODE" in
add) [ ${#args[@]} -eq 2 ] || usage; DEBS_DIR=$(cd "${args[0]}" && pwd); REPO_ARG=${args[1]} ;;
*) [ ${#args[@]} -eq 1 ] || usage; DEBS_DIR=""; REPO_ARG=${args[0]} ;;
esac
[ "$APPLY" -eq 0 ] || [ "$MODE" = remove ] || usage
if [ "$MODE" = promote ]; then
	[[ $PROMOTE_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "publish-apt: --promote needs a version like 1.1.16 (got '$PROMOTE_VERSION')" >&2; exit 2; }
fi
if [ "$MODE" = remove ] && [ -z "$REMOVE_GLOB" ]; then echo "publish-apt: --remove needs a non-empty version glob" >&2; exit 2; fi
REPO_DIR=$(cd "$REPO_ARG" && pwd)
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
CODE_DIR=${CODE_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}
APT_URL=${APT_URL:-https://w1jar.github.io/nodespot}
APT_URL=${APT_URL%/}
KEEP=${KEEP:-5}
ARCHES="arm64 amd64"
# armhf is no longer built (ASL3 has no armhf packages). The armhf debs that
# are already in the pool stay and stay indexed (frozen: nothing is added,
# promoted or pruned for armhf), so existing 32-bit installs keep a valid
# repository (no apt errors) and simply see no newer version.
LEGACY_ARCHES="armhf"
INDEX_ARCHES="$ARCHES $LEGACY_ARCHES"
COMPONENT=main
POOL_STABLE=pool/main/a/nodespot
POOL_BETA=pool/beta/a/nodespot

for t in apt-ftparchive gpg dpkg-deb python3; do
	command -v "$t" >/dev/null || { echo "publish-apt: missing required tool: $t" >&2; exit 1; }
done

key=${GPG_KEY_ID:-}
if [ -z "$key" ]; then
	key=$(gpg --batch --list-secret-keys --with-colons | awk -F: '$1=="fpr"{print $10; exit}')
fi
[ -n "$key" ] || { echo "publish-apt: no GPG secret key available (set GPG_KEY_ID / import the signing key)" >&2; exit 1; }

cd "$REPO_DIR"

mkdir -p "$POOL_STABLE" "$POOL_BETA"

# newest version in a pool directory ("" when empty)
newest_in() {
	local best="" f v
	for f in "$@"; do
		[ -e "$f" ] || continue
		v=$(dpkg-deb -f "$f" Version)
		if [ -z "$best" ] || dpkg --compare-versions "$v" gt "$best"; then best=$v; fi
	done
	printf '%s' "$best"
}
# deb of VERSION for ARCH in POOL ("" when there is none)
deb_of() { # deb_of POOL VERSION ARCH
	local f
	for f in "$1"/nodespot_*_"$3".deb; do
		[ -e "$f" ] || continue
		if [ "$(dpkg-deb -f "$f" Version)" = "$2" ]; then printf '%s' "$f"; return 0; fi
	done
	return 0
}

released=""
case "$MODE" in
add)
	# --- 1. add the new packages (always to the beta pool) ----------------------
	n=0
	for f in "$DEBS_DIR"/nodespot_*.deb; do
		[ -e "$f" ] || continue
		v=$(dpkg-deb -f "$f" Version)
		arch=$(dpkg-deb -f "$f" Architecture)
		case " $LEGACY_ARCHES " in
		*" $arch "*)
			echo "publish-apt: $f is $arch, which is no longer published; skipping it" >&2
			continue
			;;
		esac
		if [ -n "$(deb_of "$POOL_STABLE" "$v" "$arch")" ]; then
			echo "publish-apt: $v ($arch) is already promoted to stable; refusing to publish another build of it" >&2
			exit 1
		fi
		cp -f "$f" "$POOL_BETA/"
		if [ -z "$released" ] || dpkg --compare-versions "$v" gt "$released"; then released=$v; fi
		n=$((n + 1))
	done
	[ "$n" -gt 0 ] || { echo "publish-apt: no nodespot_*.deb in $DEBS_DIR" >&2; exit 1; }
	echo "publish-apt: added $n package(s) to the beta pool, version $released"
	;;
promote)
	# --- 1. move the published debs of the version from the beta to the stable pool
	released=$PROMOTE_VERSION
	missing=""
	moves=0
	for arch in $ARCHES; do
		if [ -n "$(deb_of "$POOL_BETA" "$released" "$arch")" ]; then
			moves=$((moves + 1))
		elif [ -z "$(deb_of "$POOL_STABLE" "$released" "$arch")" ]; then
			missing="$missing $arch"
		fi
	done
	if [ -n "$missing" ]; then
		echo "publish-apt: $released is not published in the beta suite for:$missing (nothing promoted)" >&2
		echo "publish-apt: tag v$released first and wait for the Release workflow to finish" >&2
		exit 1
	fi
	if [ "$moves" -eq 0 ]; then
		echo "publish-apt: $released is already in the stable suite: nothing to do"
		exit 0
	fi
	for arch in $ARCHES; do
		src=$(deb_of "$POOL_BETA" "$released" "$arch")
		[ -n "$src" ] || continue
		if [ -n "$(deb_of "$POOL_STABLE" "$released" "$arch")" ]; then
			echo "publish-apt: $released ($arch) is already in stable; dropping the beta copy"
			rm -f -- "$src"
		else
			echo "publish-apt: promoting $src"
			mv -f -- "$src" "$POOL_STABLE/"
		fi
	done
	;;
remove)
	# --- 1. delete beta-pool versions matching the glob (stable is never touched)
	matches=()
	for f in "$POOL_BETA"/nodespot_*.deb; do
		[ -e "$f" ] || continue
		v=$(dpkg-deb -f "$f" Version)
		# shellcheck disable=SC2053 # the glob is the point
		if [[ $v == $REMOVE_GLOB ]]; then matches+=("$f"); fi
	done
	echo "publish-apt: ${#matches[@]} beta-pool package(s) match '$REMOVE_GLOB' (the stable pool is never touched):"
	for f in "${matches[@]}"; do echo "  $f ($(dpkg-deb -f "$f" Version))"; done
	# what would remain
	remaining=()
	for f in "$POOL_STABLE"/nodespot_*.deb "$POOL_BETA"/nodespot_*.deb; do
		[ -e "$f" ] || continue
		skip=0
		for m in "${matches[@]}"; do [ "$f" != "$m" ] || skip=1; done
		[ "$skip" -eq 1 ] || remaining+=("$f")
	done
	if [ "${#remaining[@]}" -eq 0 ]; then
		echo "publish-apt: refusing: nothing would be left in the pool" >&2
		exit 1
	fi
	if [ -n "${REQUIRE_VERSION:-}" ]; then
		found=0
		for f in "${remaining[@]}"; do [ "$(dpkg-deb -f "$f" Version)" != "$REQUIRE_VERSION" ] || found=1; done
		if [ "$found" -eq 0 ]; then
			echo "publish-apt: refusing: $REQUIRE_VERSION would no longer be in the pool (publish it first, or fix the glob)" >&2
			exit 1
		fi
		echo "publish-apt: $REQUIRE_VERSION stays in the pool"
	fi
	if [ "$APPLY" -ne 1 ]; then
		echo "publish-apt: dry run, nothing changed (use --apply to delete)"
		exit 0
	fi
	for f in "${matches[@]}"; do rm -f -- "$f"; done
	# republish the newest remaining version as such (refreshes install.sh and the notes)
	released=$(newest_in "${remaining[@]}")
	;;
esac

# --- 2. retention: newest $KEEP versions per architecture, per pool directory --
# (separately, so unpromoted builds never push a release out of the pool; the
# version just added or promoted is never pruned, whatever its age)
if [ "$MODE" != remove ]; then
	for pool in "$POOL_STABLE" "$POOL_BETA"; do
		for arch in $ARCHES; do
			files=()
			versions=()
			for f in "$pool"/nodespot_*_"$arch".deb; do
				[ -e "$f" ] || continue
				files+=("$f")
				versions+=("$(dpkg-deb -f "$f" Version)")
			done
			# selection sort, newest first (dpkg version order, so 0.10.0 > 0.9.0 and ~ works)
			count=${#files[@]}
			for ((i = 0; i < count; i++)); do
				best=$i
				for ((j = i + 1; j < count; j++)); do
					if dpkg --compare-versions "${versions[j]}" gt "${versions[best]}"; then best=$j; fi
				done
				if [ "$best" -ne "$i" ]; then
					tf=${files[i]}; files[i]=${files[best]}; files[best]=$tf
					tv=${versions[i]}; versions[i]=${versions[best]}; versions[best]=$tv
				fi
			done
			for ((i = KEEP; i < count; i++)); do
				[ "${versions[i]}" != "$released" ] || continue
				echo "publish-apt: pruning ${files[i]}"
				rm -f -- "${files[i]}"
			done
		done
	done
fi

newest_stable=$(newest_in "$POOL_STABLE"/nodespot_*.deb)
newest_all=$(newest_in "$POOL_STABLE"/nodespot_*.deb "$POOL_BETA"/nodespot_*.deb)
[ -n "$newest_stable" ] || echo "publish-apt: note: no stable version in the pool yet"

# --- 3. install.sh is refreshed only for the newest version in the pool -------
# (a 1.0.x backport published after 1.1.x must not revert it). Promotion never
# refreshes it (it was refreshed when the version was published to beta). The
# README, licence, SECURITY.md and issue forms live on main, not here.
sync_root=0
if [ "$MODE" = promote ]; then
	echo "publish-apt: promotion: not refreshing install.sh"
elif dpkg --compare-versions "$released" ge "$newest_all"; then
	sync_root=1
else
	echo "publish-apt: $released is older than the newest version in the pool ($newest_all): not refreshing install.sh"
fi
touch .nojekyll # serve dotfiles and paths verbatim

# --- 4. package indexes and signed Release, per suite --------------------------
conf=$(mktemp)
trap 'rm -f "$conf"' EXIT
publish_suite() { # publish_suite SUITE POOLROOT
	local suite=$1 root=$2 arch d
	for arch in $INDEX_ARCHES; do
		d="dists/$suite/$COMPONENT/binary-$arch"
		mkdir -p "$d"
		apt-ftparchive --arch "$arch" packages "$root" >"$d/Packages"
		gzip -9nkf "$d/Packages"
	done
	cat >"$conf" <<EOC
APT::FTPArchive::Release::Origin "nodespot";
APT::FTPArchive::Release::Label "nodespot";
APT::FTPArchive::Release::Suite "$suite";
APT::FTPArchive::Release::Codename "$suite";
APT::FTPArchive::Release::Architectures "$INDEX_ARCHES";
APT::FTPArchive::Release::Components "$COMPONENT";
APT::FTPArchive::Release::Description "nodespot packages for Debian 12 (bookworm), Debian 13 (trixie) and Raspberry Pi OS ($suite)";
EOC
	apt-ftparchive -c "$conf" release "dists/$suite" >"dists/$suite/Release.new"
	mv -f "dists/$suite/Release.new" "dists/$suite/Release"
	rm -f "dists/$suite/InRelease" "dists/$suite/Release.gpg"
	gpg --batch --yes --default-key "$key" --clearsign -o "dists/$suite/InRelease" "dists/$suite/Release"
	gpg --batch --yes --default-key "$key" --armor --detach-sign -o "dists/$suite/Release.gpg" "dists/$suite/Release"
}
publish_suite stable pool/main
publish_suite beta pool

# --- 5. public key -----------------------------------------------------------
gpg --batch --yes --export "$key" >nodespot-archive-keyring.gpg
gpg --batch --yes --armor --export "$key" >nodespot-archive-keyring.asc
fpr=$(gpg --batch --with-colons --fingerprint "$key" | awk -F: '$1=="fpr"{print $10; exit}')

# --- 6. latest.json / latest-beta.json (version, release notes, date) ----------
# Notes of a version: NOTES_FILE or the tag annotation (only for the version
# being released), else its CHANGELOG section ("## [1.1.16]"). When nothing is
# found the notes already in the json for the same version are kept.
tag_version=""
[ -z "${TAG:-}" ] || tag_version=${TAG#v}
notes_for() { # notes_for VERSION
	local v=$1 notes=""
	if [ "$v" = "$released" ]; then
		if [ -n "${NOTES_FILE:-}" ] && [ -f "$NOTES_FILE" ]; then
			notes=$(cat "$NOTES_FILE")
		elif [ "$v" = "$tag_version" ] && [ -d "$CODE_DIR/.git" ]; then
			notes=$(git -C "$CODE_DIR" tag -l --format='%(contents)' "$TAG" 2>/dev/null || true)
		fi
	fi
	if [ -z "${notes//[[:space:]]/}" ] && [ -f "$CODE_DIR/CHANGELOG.md" ]; then
		# section "## [X.Y.Z] ..." or "## X.Y.Z ..." up to the next "## "
		notes=$(awk -v v="$v" '
			/^## / { if (on) exit; line=$0; gsub(/[\[\]]/, "", line); split(line, p, " "); if (p[2] == v) { on=1; next } }
			on { print }' "$CODE_DIR/CHANGELOG.md")
	fi
	printf '%s' "$notes"
}
write_latest() { # write_latest FILE VERSION
	local file=$1 v=$2
	[ -n "$v" ] || return 0
	JSON_FILE=$file VERSION_NEWEST=$v NOTES=$(notes_for "$v") python3 -I - <<'EOPY'
import json, os, datetime
path, version = os.environ["JSON_FILE"], os.environ["VERSION_NEWEST"]
notes = os.environ.get("NOTES", "").strip()
date = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
try:
    with open(path) as f:
        old = json.load(f)
except (OSError, ValueError):
    old = {}
if old.get("version") == version:
    # same version as before: keep its date, and its notes if none were found
    date = old.get("date") or date
    notes = notes or (old.get("notes") or "").strip()
with open(path + ".new", "w") as f:
    print(json.dumps({"version": version, "notes": notes, "date": date}, indent=2), file=f)
os.replace(path + ".new", path)
EOPY
}
write_latest latest.json "$newest_stable"
write_latest latest-beta.json "$newest_all"

# --- 7. install script and landing page --------------------------------------
# The script already defaults to the standard URL; rewrite it if APT_URL differs.
if [ "$sync_root" -eq 1 ] || [ ! -f install.sh ]; then
	sed "s|https://w1jar.github.io/nodespot|$APT_URL|g" "$SCRIPT_DIR/install.sh" >install.sh
	chmod 0755 install.sh
fi
beta_note=""
[ "$newest_all" != "$newest_stable" ] || beta_note=" (same as stable)"
# The page is a quoted heredoc (no shell expansion: the CSS/JS can use $ and
# backticks freely); the @PLACEHOLDERS@ are filled in below. The replacement is
# quoted, so characters such as & in a value are copied literally.
read -r -d '' page <<'EOH' || true
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>nodespot - native radio node and web control panel</title>
<meta name="description" content="Install nodespot, a native radio node (AllStarLink, EchoLink and digital modes, no Asterisk) with a web control panel, from the signed apt repository.">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='8' fill='%230ea5e9'/%3E%3Cpath d='M8 22a11 11 0 0 1 16 0M11.5 18.5a6 6 0 0 1 9 0' stroke='%23fff' stroke-width='2.4' fill='none' stroke-linecap='round'/%3E%3Ccircle cx='16' cy='23' r='2' fill='%23fff'/%3E%3C/svg%3E">
<style>
:root{--bg:#f3f5f8;--surface:#fff;--surface-2:#e9edf2;--line:#cfd6df;--fg:#0f172a;--muted:#566274;--accent:#0369a1;--accent-fg:#fff;--ok:#15803d;--warn:#a16207;--danger:#b91c1c;
--sans:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;--mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color-scheme:light}
@media (prefers-color-scheme:dark){:root{--bg:#0a0e14;--surface:#111821;--surface-2:#19222e;--line:#273344;--fg:#e6edf6;--muted:#93a1b5;--accent:#38bdf8;--accent-fg:#04121c;--ok:#4ade80;--warn:#facc15;--danger:#f87171;color-scheme:dark}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.55 var(--sans)}
a{color:var(--accent)}
:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:3px}
.wrap{max-width:760px;margin:0 auto;padding:0 16px}
header.top{background:var(--surface);border-bottom:1px solid var(--line)}
header.top .wrap{display:flex;align-items:center;gap:10px;padding-top:10px;padding-bottom:10px;flex-wrap:wrap}
.brand{display:flex;align-items:center;gap:6px;font-weight:700;font-size:16px;letter-spacing:-.025em;color:var(--fg);text-decoration:none}
.brand svg{width:18px;height:18px;flex:none;color:var(--accent)}
header.top .gh{margin-left:auto}
.hero{padding:28px 0 4px}
h1{font-size:24px;line-height:1.25;margin:0 0 8px;letter-spacing:-.015em}
h2{font-size:17px;margin:0 0 8px;letter-spacing:-.01em}
h3{font-size:14px;margin:18px 0 6px}
p{margin:0 0 10px}
.lead{color:var(--muted);font-size:16px;margin-bottom:12px}
.muted{color:var(--muted)}
.badges{display:flex;flex-wrap:wrap;gap:6px;margin:0 0 4px;padding:0;list-style:none}
.badge{display:inline-flex;align-items:center;gap:4px;border:1px solid var(--line);background:var(--surface-2);border-radius:999px;padding:1px 10px;font-size:12px;font-weight:500;color:var(--muted)}
.badge b{color:var(--fg);font-weight:600}
.card{background:var(--surface);border:1px solid var(--line);border-radius:6px;padding:16px;margin:16px 0}
.card>:last-child{margin-bottom:0}
.note{border-left:3px solid var(--accent);background:var(--surface-2);border-radius:0 6px 6px 0;padding:8px 12px;margin:0 0 10px;font-size:14px}
.note.warn{border-left-color:var(--warn)}
code{font-family:var(--mono);font-size:.9em}
p code,li code,label code,.hint code{background:var(--surface-2);border:1px solid var(--line);border-radius:4px;padding:0 4px;word-break:break-word}
.cmd{position:relative;margin:0 0 10px;background:var(--bg);border:1px solid var(--line);border-radius:6px}
.cmd pre{margin:0 2.75rem 0 0;background:none;border:0;border-radius:6px;padding:10px 12px;overflow-x:auto;max-width:100%;font-family:var(--mono);font-size:13px;line-height:1.5}
.cmd pre code{font-size:inherit;background:none;border:0;padding:0;word-break:normal;white-space:pre}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:4px;min-height:32px;border-radius:4px;border:1px solid var(--line);background:var(--surface);color:var(--fg);padding:2px 12px;font:500 13px var(--sans);cursor:pointer;text-decoration:none;transition:background .15s}
.btn:hover{background:var(--surface-2)}
.cmd pre:focus-visible{outline-offset:-2px}
.copy{position:absolute;top:6px;right:6px;display:inline-flex;align-items:center;justify-content:center;width:32px;height:32px;padding:0;border-radius:6px;border:1px solid var(--line);background:var(--surface);color:var(--muted);cursor:pointer;transition:background .15s,color .15s,border-color .15s}
.copy:hover{background:var(--surface-2);color:var(--fg)}
.copy[disabled]{opacity:.4;cursor:not-allowed;background:transparent;color:var(--muted)}
.copy[disabled]:hover{background:transparent;color:var(--muted)}
.copy .i-check{display:none}
.copy.done{color:var(--ok);border-color:var(--ok)}
.copy.done .i-copy{display:none}
.copy.done .i-check{display:block}
.copy.fail{color:var(--warn);border-color:var(--warn)}
.row{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
fieldset{border:0;margin:0 0 16px;padding:0;min-width:0}
legend,.label{display:block;padding:0;margin:0 0 4px;font-size:12px;font-weight:600;color:var(--muted)}
.opt{display:flex;align-items:flex-start;gap:8px;padding:2px 0}
.opt input{margin:4px 0 0;width:16px;height:16px;accent-color:var(--accent);flex:none}
.opt label{font-size:14px;color:var(--fg)}
.opt small{display:block;color:var(--muted);font-size:13px}
.input{display:block;width:9rem;min-height:32px;border-radius:4px;border:1px solid var(--line);background:var(--surface);color:var(--fg);padding:2px 10px;font:14px var(--sans)}
.input::placeholder{color:var(--muted)}
.input[aria-invalid=true]{border-color:var(--danger)}
.hint{margin:4px 0 0;font-size:13px;color:var(--muted)}
.err{margin:4px 0 0;font-size:13px;color:var(--danger);font-weight:500}
.warnmsg{margin:4px 0 0;font-size:13px;color:var(--warn)}
ul.plain{margin:0 0 10px;padding-left:20px}
ul.plain li{margin:0 0 4px}
dl.params{margin:0}
dl.params dt{font-weight:600;margin-top:10px}
dl.params dd{margin:2px 0 0;color:var(--muted)}
footer{padding:8px 0 40px;font-size:13px;color:var(--muted)}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
[hidden]{display:none!important}
@media (prefers-reduced-motion:reduce){*{transition:none!important}}
</style>
</head><body>
<header class="top"><div class="wrap">
<a class="brand" href="#top" aria-label="nodespot"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4.9 16.1C1 12.2 1 5.8 4.9 1.9"/><path d="M7.8 4.7a6.14 6.14 0 0 0-.8 7.5"/><circle cx="12" cy="9" r="2"/><path d="M16.2 4.8c2 2 2.26 5.11.8 7.47"/><path d="M19.1 1.9a9.96 9.96 0 0 1 0 14.1"/><path d="M9.5 18h5"/><path d="m8 22 4-11 4 11"/></svg>nodespot</a>
<a class="btn gh" href="https://github.com/w1jar/nodespot">GitHub</a>
</div></header>
<main class="wrap" id="top">
<section class="hero">
<h1>A native radio node with a web control panel</h1>
<p class="lead">nodespot lets you connect, monitor and manage favorites with live status from any browser, and it can set up digital modes for you. It runs on Debian 12 (bookworm), Debian 13 (trixie) and Raspberry Pi OS (arm64 and amd64; 64-bit only).</p>
<ul class="badges">
<li class="badge">Latest release <b>@STABLE@</b></li>
<li class="badge">Latest beta <b>@BETA@</b>@BETA_NOTE@</li>
</ul>
<p class="muted">Docs, screenshots and bug reports are on <a href="https://github.com/w1jar/nodespot">GitHub</a>.</p>
</section>

<section class="card" aria-labelledby="h-quick">
<h2 id="h-quick">Quick install</h2>
<p>Run this on the node (over SSH or in a terminal):</p>
<div class="cmd">
<pre tabindex="0"><code id="cmd-default">curl -fsSL @APT_URL@/install.sh | sudo sh</code></pre><button type="button" class="copy" data-copy="cmd-default" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button>
</div>
<p class="note">Run this on the node (over SSH or in a terminal). It works on plain Debian 12/13 or Raspberry Pi OS Lite 64-bit. nodespot runs its own node: it does not need AllStarLink 3 or Asterisk.</p>
<p class="note warn">On a freshly flashed card, wait for its first-boot setup to finish before you install. The installer checks this.</p>
<p class="note">At the end the installer prints a one-time setup link. Open it to create the administrator or to restore a backup from an existing node. <code>sudo nodespot admin setup-code</code> shows the code again.</p>
</section>

<section class="card" aria-labelledby="h-opts">
<h2 id="h-opts">Install options</h2>
<div id="wizard" hidden>
<p class="muted">Pick what you need and copy the command. Everything is optional.</p>
<form id="opts" onsubmit="return false" novalidate>
<fieldset>
<legend>Port 80 and other web services</legend>
<div class="opt"><input type="checkbox" id="o-take"><label for="o-take">Run nodespot on port 80 and turn off web services an earlier ASL install left<small>Stops and disables Allmon3, the web server of the ASL web UIs (Apache, lighttpd or nginx), asl-web-ui and Supermon, if they are there. The panel is then at <code>http://your-node/</code>. You can undo it (see below).</small></label></div>
</fieldset>
<fieldset>
<legend>Web port</legend>
<label class="label" for="o-port">Port (1024-65535, or 80)</label>
<input class="input" id="o-port" type="number" inputmode="numeric" min="80" max="65535" step="1" placeholder="8181" aria-describedby="o-port-hint o-port-err" autocomplete="off">
<p class="hint" id="o-port-hint">1024-65535, or 80; 8080 is the default. Avoid 9090 (Cockpit). Port 80 is set for you by the option above.</p>
<p class="err" id="o-port-err" role="alert" hidden></p>
<p class="warnmsg" id="o-port-warn" hidden></p>
</fieldset>
<fieldset>
<legend>Channel</legend>
<div class="opt"><input type="radio" name="chan" id="o-stable" value="stable" checked><label for="o-stable">Stable<small>Tested releases. This is the default.</small></label></div>
<div class="opt"><input type="radio" name="chan" id="o-beta" value="beta"><label for="o-beta">Beta<small>New builds before they are promoted to stable. You get fixes sooner, but they may be less stable.</small></label></div>
</fieldset>
</form>
<h3>Your command</h3>
<div class="cmd">
<pre tabindex="0"><code id="cmd-gen">curl -fsSL @APT_URL@/install.sh | sudo sh</code></pre><button type="button" class="copy" id="copy-gen" data-copy="cmd-gen" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button>
</div>
</div>
<div id="params-static">
<p>Add these before <code>sh</code>, after <code>sudo</code>, separated by spaces. All are optional:</p>
<dl class="params">
<dt><code>NODESPOT_PORT=8181</code></dt><dd>Web port, 1024-65535, or 80. The default is 8080. Avoid 9090 (Cockpit).</dd>
<dt><code>NODESPOT_TAKEOVER=1</code></dt><dd>Run nodespot on port 80 and turn off web services an earlier ASL install left (<code>yes</code> and <code>true</code> work too). See below.</dd>
<dt><code>NODESPOT_CHANNEL=beta</code></dt><dd>Use the beta channel (new builds before they are promoted to stable). The default is <code>stable</code>.</dd>
</dl>
<p style="margin-top:10px">Example with several options:</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-example">curl -fsSL @APT_URL@/install.sh | sudo NODESPOT_PORT=8181 NODESPOT_CHANNEL=beta sh</code></pre><button type="button" class="copy" data-copy="cmd-example" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
</div>
</section>

<section class="card" aria-labelledby="h-take">
<h2 id="h-take">nodespot on port 80, no other web UI</h2>
<p>A card that ran ASL3 before may still run Apache, Allmon3 and the ASL web UI. To make nodespot the only web UI, on port 80:</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-take">curl -fsSL @APT_URL@/install.sh | sudo NODESPOT_TAKEOVER=1 sh</code></pre><button type="button" class="copy" data-copy="cmd-take" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
<p>The installer stops and disables Allmon3, the web server of the ASL web UIs (Apache, lighttpd or nginx), asl-web-ui and Supermon, but only those that exist and are on, and lists each one. <strong>The node keeps running.</strong> Nothing is uninstalled. Then nodespot moves to port 80 and the panel is at <code>http://your-node/</code>. If something else still holds port 80, the installer names it and leaves nodespot on its old port.</p>
<p>To undo it (nodespot goes back to its old port and the units it turned off are enabled and started again):</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-untake">curl -fsSL @APT_URL@/install.sh | sudo NODESPOT_TAKEOVER=undo sh</code></pre><button type="button" class="copy" data-copy="cmd-untake" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
</section>

<section class="card" aria-labelledby="h-later">
<h2 id="h-later">Changing things later</h2>
<h3>Web port</h3>
<p>The web interface listens on port <strong>8080</strong> by default. To change it, run the installer again with <code>NODESPOT_PORT</code> (1024-65535, or 80), or edit <code>listen</code> in <code>/etc/nodespot/nodespot.conf</code> (for example <code>listen = :8181</code>) and run <code>sudo systemctl restart nodespot</code>. The installer warns if another program already uses the port.</p>
<p>If firewalld is running, the installer and the package open the configured port for you. With another firewall (ufw, nftables, a router) open the TCP port yourself. If you edit <code>listen</code> by hand while firewalld is running, open the new port too.</p>
<h3>Update</h3>
<p>Use Settings &rarr; Updates in the web UI, or:</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-update">sudo apt update &amp;&amp; sudo apt upgrade</code></pre><button type="button" class="copy" data-copy="cmd-update" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
<h3>Uninstall</h3>
<div class="cmd"><pre tabindex="0"><code id="cmd-remove">sudo apt remove nodespot   # keeps users and favorites
sudo apt purge nodespot    # also deletes them</code></pre><button type="button" class="copy" data-copy="cmd-remove" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
<p><code>apt purge</code> also deletes the node config (which holds network passwords).</p>
</section>

<section class="card" aria-labelledby="h-manual">
<h2 id="h-manual">Manual apt setup</h2>
<p>Prefer not to pipe a script? Add the signed repository yourself:</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-manual">sudo curl -fsSL -o /usr/share/keyrings/nodespot-archive-keyring.gpg @APT_URL@/nodespot-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/nodespot-archive-keyring.gpg] @APT_URL@ stable main" | sudo tee /etc/apt/sources.list.d/nodespot.list
sudo apt update &amp;&amp; sudo apt install nodespot</code></pre><button type="button" class="copy" data-copy="cmd-manual" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
<p>Signing key fingerprint: <code>@FPR@</code> (<a href="nodespot-archive-keyring.asc">armored key</a>)</p>
<h3>Beta channel</h3>
<p>Every new version goes to the <code>beta</code> suite as soon as it is built. It becomes a release (suite <code>stable</code>) when it is promoted after testing: the same package, not a rebuild. The <code>beta</code> suite also carries every release, so beta users are never behind. Betas may be less stable.</p>
<p>To use it, put <code>beta</code> instead of <code>stable</code> in the line above:</p>
<div class="cmd"><pre tabindex="0"><code id="cmd-beta">echo "deb [signed-by=/usr/share/keyrings/nodespot-archive-keyring.gpg] @APT_URL@ beta main" | sudo tee /etc/apt/sources.list.d/nodespot.list</code></pre><button type="button" class="copy" data-copy="cmd-beta" aria-label="Copy command" title="Copy" hidden><svg class="i-copy" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg><svg class="i-check" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></div>
<p>Or install with <code>NODESPOT_CHANNEL=beta</code> (see the options above), or switch in Settings &rarr; Updates in the web UI. It is the same setting: it changes the suite in <code>/etc/apt/sources.list.d/nodespot.list</code>. Going back to <code>stable</code> does not downgrade: the node stays on the installed version until a release newer than it is promoted.</p>
</section>
</main>
<footer><div class="wrap">nodespot &middot; <a href="https://github.com/w1jar/nodespot">GitHub</a> &middot; <a href="https://github.com/w1jar/nodespot/issues">Report a bug</a></div></footer>
<div class="sr" id="live" aria-live="polite" role="status"></div>
<script>
(function () {
  var BASE = "@APT_URL@";
  var live = document.getElementById("live");
  function copyText(el) {
    var text = el.textContent;
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).catch(function () { return fallback(el); });
    }
    return fallback(el);
  }
  function fallback(el) {
    return new Promise(function (ok, fail) {
      try {
        var sel = window.getSelection(), r = document.createRange();
        r.selectNodeContents(el); sel.removeAllRanges(); sel.addRange(r);
        if (document.execCommand("copy")) ok(); else fail(new Error("copy failed"));
      } catch (e) { fail(e); }
    });
  }
  var btns = document.querySelectorAll("[data-copy]");
  Array.prototype.forEach.call(btns, function (b) {
    b.hidden = false;
    b.addEventListener("click", function () {
      var el = document.getElementById(b.getAttribute("data-copy"));
      if (!el) return;
      copyText(el).then(function () { flash(b, "done", "Copied to clipboard"); },
        function () { flash(b, "fail", "Copy failed, the command is selected: press Ctrl+C"); });
    });
  });
  function flash(b, cls, say) {
    b.classList.remove("done", "fail");
    b.classList.add(cls);
    b.title = cls === "done" ? "Copied" : "Press Ctrl+C";
    live.textContent = "";
    setTimeout(function () { live.textContent = say; }, 30);
    clearTimeout(b._t);
    b._t = setTimeout(function () { b.classList.remove("done", "fail"); b.title = "Copy"; }, 2000);
  }

  var wiz = document.getElementById("wizard"), stat = document.getElementById("params-static");
  var port = document.getElementById("o-port"),
    beta = document.getElementById("o-beta"), take = document.getElementById("o-take"), out = document.getElementById("cmd-gen"),
    err = document.getElementById("o-port-err"), warn = document.getElementById("o-port-warn"),
    copyGen = document.getElementById("copy-gen");
  wiz.hidden = false; stat.hidden = true;
  function build() {
    var raw = port.value.trim(), bad = port.validity && port.validity.badInput;
    var valid = !bad && (raw === "" || raw === "80" || (/^[1-9][0-9]{3,4}$/.test(raw) && Number(raw) >= 1024 && Number(raw) <= 65535));
    port.setAttribute("aria-invalid", valid ? "false" : "true");
    err.hidden = valid;
    err.textContent = valid ? "" : "Enter 80 or a whole number from 1024 to 65535, or leave it empty for 8080.";
    copyGen.disabled = !valid;
    var w = "";
    if (valid && raw === "9090") w = "Port 9090 is used by Cockpit. Pick another port.";
    warn.textContent = w; warn.hidden = !w;
    if (!valid) return;
    var vars = [];
    if (take.checked) vars.push("NODESPOT_TAKEOVER=1");
    if (raw !== "" && raw !== "8080" && !(take.checked && raw === "80")) vars.push("NODESPOT_PORT=" + raw);
    if (beta.checked) vars.push("NODESPOT_CHANNEL=beta");
    out.textContent = "curl -fsSL " + BASE + "/install.sh | sudo " + (vars.length ? vars.join(" ") + " " : "") + "sh";
  }
  document.getElementById("opts").addEventListener("input", build);
  document.getElementById("opts").addEventListener("change", build);
  build();
})();
</script>
</body></html>
EOH
page=${page//@APT_URL@/"$APT_URL"}
page=${page//@STABLE@/"${newest_stable:-none yet}"}
page=${page//@BETA@/"$newest_all"}
page=${page//@BETA_NOTE@/"$beta_note"}
page=${page//@FPR@/"$fpr"}
printf '%s
' "$page" >index.html
touch .nojekyll
echo "publish-apt: repository updated (released $released, stable ${newest_stable:-none}, beta channel $newest_all, key $fpr)"
