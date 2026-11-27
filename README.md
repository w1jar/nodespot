# nodespot

All-in-one multi-mode radio node for Raspberry Pi and mini PCs.

nodespot continues the archived [aslcp](https://github.com/w1jar/aslcp) project (final release 2.4). It is a fresh start with new package, service and path names, and there is no upgrade path from aslcp: install nodespot fresh.

nodespot runs its own node (`nodespot-node`): one radio (a SHARI or another SA818 interface) and one network mode at a time, AllStarLink, EchoLink or a digital mode (DMR, YSF, P25, NXDN). Whatever mode is active, its audio goes out on the radio; networks are never bridged to each other. It does not need Asterisk or AllStarLink 3 (ASL3) and never installs them. The web page picks the mode and target, shows who is calling, keeps favorites per mode, runs schedules, tunes the radio and backs up and restores the node.

> nodespot is a new design. Parts of this README below the install section still describe the aslcp 2.x panel for ASL3 and are being rewritten.

![nodespot Dashboard, desktop](docs/images/dashboard-desktop.png)

## Features

- **Live dashboard** for every local node: links, keyed state (who is transmitting right now), connection time, mode, node information and the wider network, updated about once a second.
- **Connect, disconnect, monitor and local monitor**, with permanent links and an optional "disconnect before connect" so you do not bridge two networks by accident.
- **Modes page** (the Modes item in the top bar): a button for each mode (AllStar, EchoLink, BrandMeister, TGIF, DMR, YSF, P25, NXDN, M17 and the echo test) with the active one highlighted and not-ready ones marked. Pick a button to see that mode: status and who is calling, connect and disconnect with host-list or node search, the last tune, per-mode favorites (add, remove, move, connect, pin to the dashboard) and its settings in a collapsible section, with the AMBE vocoder install for the modes that need it. Making a mode active is an explicit button; connecting a target also activates it. Favorites pinned to the dashboard connect in one tap and switch the mode if needed. The old Settings > Modes routes redirect here (`/settings/modes/<mode>` opens `/modes?mode=<mode>`).
- **Favorites** with groups and ordering; import from AllScan. An optional scan shows which favorites are active.
- **Node search** by number, callsign, description or location in the connect bar and favorites, using the AllStarLink node list.
- **Digital modes** (DMR, YSF, P25, NXDN) on the built-in engine, with a Digital modes card on the dashboard, an Echo test and a **Now calling** strip. See [Digital modes](#digital-modes).
- **Schedules**: connect and disconnect nodes at set times (weekly or one-off), with a global on/off switch, an upcoming list, and a choice of what happens when you connect by hand.
- **Radio tune** (administrators): receive and transmit levels and the SA818 channel, tones, squelch and filters.
- **Backup and restore** (Settings, Maintenance): download one file with the node settings, the SA818 settings, nodespot users, favorites and schedules. Network passwords are only included when you ask. Restore it on a freshly installed node during setup or later.
- **Settings** (administrators; the Settings item in the top bar, with Station, Radio, Caller lookup, Access, System, Maintenance, About and Account; mode settings are no longer here, they are on the Modes page): CPU temperature, load, uptime, node service status and restart, ASL services, web port, WiFi, a browser terminal, updates and diagnostics.
- **Diagnostics**: download one `.tar.gz` with versions, recent logs, network summary, disk and memory, service states and your config files, to ask for help. Secrets are replaced by `<redacted>`; a checkbox (on by default) also hides your callsign and public IP. Password files, private keys and the nodespot database are never included. Read it before posting it publicly.
- **Users and roles**: admin, operator and viewer, with rate-limited login and CSRF protection.
- **Updates from the web page**: Settings, System, Updates shows the installed and available version with release notes, lets you pick the release or beta channel, and installs updates on request.
- **Built-in bug reporting**: Settings, About has a prefilled issue form, and Settings, Maintenance, Diagnostics has a diagnostics download that removes private data.
- Light and dark themes, works on phones and tablets.
- Small and safe: one static binary, runs as an unprivileged, hardened systemd service; a tiny root helper performs only package updates, firewall changes and similar system tasks.

<p>
  <img src="docs/images/dashboard-mobile.png" alt="nodespot Dashboard on a phone" width="260">
  <img src="docs/images/settings-updates.png" alt="Settings, Updates section" width="520">
</p>

### Schedules

![Schedules](docs/images/schedules.png)

## Requirements

- Debian 12 (bookworm) or Debian 13 (trixie), or Raspberry Pi OS 64-bit based on either. A fresh Raspberry Pi OS Lite 64-bit works.
- No AllStarLink 3 or Asterisk: nodespot runs its own node. On a card that already runs ASL3, stop Asterisk (`sudo systemctl disable --now asterisk`) so the native node can use the radio.
- A 64-bit system: an ARM board (arm64) or a PC (amd64). A Raspberry Pi Zero 2 W or newer running Raspberry Pi OS Lite (64-bit) is plenty. 32-bit (armhf) is not supported; on a 32-bit system the installer refuses before changing anything, so reinstall with the 64-bit OS.
- Root access (sudo) for the installation.

## Install

On the node:

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo sh
```

The script adds the signed nodespot apt repository, installs the `nodespot` package and prints a one-time setup link. Then open `http://<your-node>:8080` (8080 is the default port; see [Web port](#web-port)).

On a freshly flashed card, wait for the first-boot setup to finish before installing. The installer checks this (first-boot services, another running apt/dpkg or a held dpkg lock) and refuses to change anything until it is done; just run it again later. `NODESPOT_SKIP_PREFLIGHT=1` skips the check. A pending reboot only produces a warning. Package operations started from the web UI wait up to 10 minutes for the apt lock instead of failing.

### Raspberry Pi (no screen needed)

You can set up a Raspberry Pi from another computer, without a monitor or keyboard.

1. Install [Raspberry Pi Imager](https://www.raspberrypi.com/software/), version 2.0 or later.
2. Choose your Pi model, then the operating system: **Raspberry Pi OS (other)**, then **Raspberry Pi OS Lite (64-bit)**. Then choose your microSD card.
3. On the customisation pages, set a host name (for example `node1997`) and a user name and password. If you will not use Ethernet, also set the Wi-Fi network name, password and country. Turn on SSH; password authentication is fine. Then write the card.
4. Put the card in the Pi and power it up. Wait a few minutes, then check that it is on the network:

```sh
ping node1997.local
```

Or find its address in your router.

5. Log in:

```sh
ssh <user>@node1997.local
```

6. Run the installer. Wait for the first boot to finish first (see above).

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo sh
```

7. Open the one-time setup link that the installer prints, from any device on the same network. If you lose it, show it again:

```sh
sudo nodespot admin setup-code
```

Any Raspberry Pi that runs 64-bit Raspberry Pi OS works. 32-bit Raspberry Pi OS does not.

### Digital modes at install

Digital modes are built into the node; there is nothing to enable. `NODESPOT_DV=1`, which enabled them in 2.x, is still accepted and does nothing. Prefer to do it by hand? See the manual steps on <https://w1jar.github.io/nodespot/>.

### AMBE vocoder at install (`NODESPOT_VOCODER`)

The software AMBE vocoder, which DMR, YSF and NXDN voice need, is normally installed later from the Modes page. To install it during the install instead, run the installer with `NODESPOT_VOCODER=1` (`yes` and `true` work too; any other value is refused):

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo NODESPOT_VOCODER=1 sh
```

It uses the same code as the Modes page (`nodespot-helper vocoder-install --wait`) and runs after nodespot is installed and started; it can take a few minutes. If the vocoder cannot be installed (unsupported system, no network, apt error) the installer prints a warning and still finishes successfully; install it later from the Modes page. It cannot be combined with `NODESPOT_TAKEOVER=undo`.

### First login

The installer ends by printing a one-time setup link (`http://<your-node>:8080/#/setup?code=...`, with your port if you chose another). Open it to create the administrator, or to restore a backup from an existing node. Lost the link? Run `sudo nodespot admin setup-code`, or create the admin in the terminal with `sudo nodespot admin create`. Then log in at `http://<your-node>:8080`. Forgot the password? Run:

```sh
sudo nodespot admin reset-password <user>
```

`sudo nodespot admin list` shows the accounts.

### Web port

nodespot listens on TCP port 8080 by default. To pick another port when installing, set `NODESPOT_PORT` (1024-65535, or exactly 80; nodespot runs without privileges, so other lower ports, 443 included, are refused):

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo NODESPOT_PORT=8181 sh
```

The installer writes it to `listen` in `/etc/nodespot/nodespot.conf` (`listen = :8181`, or `listen = 192.0.2.10:8181` to bind one address), restarts nodespot and warns if another program already uses the port. To change it later, rerun the installer the same way, or edit `listen` and run `sudo systemctl restart nodespot`. Port 80 needs a systemd drop-in granting `CAP_NET_BIND_SERVICE`; the installer writes it for `NODESPOT_PORT=80` (only then; default installs stay unchanged), or let nodespot write it for you in the System tab (below). nodespot serves plain HTTP, so 443 is not offered.

#### Port 80

Administrators can move nodespot to port 80 in **Settings, System, Web port**, so the panel answers on `http://<your-node>/`. Port 80 must be free: if Apache runs, turn it off first in **Settings, System, ASL web services**. The move writes a systemd drop-in (`/etc/systemd/system/nodespot.service.d/port80.conf`) and changes `listen`, then restarts nodespot. The page shows the new address; open it within 60 seconds to confirm. If you do not, nodespot goes back to its old port by itself, so you cannot lock yourself out. The old port is closed after the move (old bookmarks stop working), and **Move back** undoes it. While nodespot is on port 80, Apache cannot be turned back on.

#### Port 80 and the other ASL web services at install (`NODESPOT_TAKEOVER`)

To make nodespot the only web UI of the node, on port 80, run the installer with `NODESPOT_TAKEOVER=1` (`yes` and `true` work too; any other value is refused):

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo NODESPOT_TAKEOVER=1 sh
```

It implies `NODESPOT_PORT=80` unless you give `NODESPOT_PORT` yourself. After the package is installed, the installer stops and disables the ASL web services that would compete with nodespot, but only those that exist on the node and are enabled or running, and prints each one:

- Allmon3 (`allmon3.service`);
- the web server of the ASL web UIs: `apache2.service`, `lighttpd.service` or `nginx.service`, whichever is there (the ASL3 appliance image uses Apache for its landing page and for asl-web-ui);
- the asl-web-ui backend (`asl-web-ui.service`) and Supermon (`supermon.service`, `supermon2.service`, `supermon-ng.service`), if they run as services.

Asterisk and app_rpt keep running, and so does everything else the node needs to stay on the air; the node is not taken off the air. Cockpit (port 9090) does not compete with nodespot and is left as it is; turn it off in **Settings, System, ASL web services** if you want to. Nothing is uninstalled.

Then nodespot moves to port 80 (drop-in `/etc/systemd/system/nodespot.service.d/port80.conf` with `AmbientCapabilities` and `CapabilityBoundingSet` = `CAP_NET_BIND_SERVICE`, `listen = :80`, port 80 opened in firewalld) and the panel is at `http://<your-node>/`. If another program still holds port 80 after the takeover (for example a web server the installer does not know), the installer names it, does not move nodespot, finishes on the old port and exits with status 1; the services it turned off stay off until you undo.

What was turned off is recorded in `/var/lib/nodespot/takeover.list`. To undo, run the installer once more:

```sh
curl -fsSL https://w1jar.github.io/nodespot/install.sh | sudo NODESPOT_TAKEOVER=undo sh
```

nodespot moves back to the port it had before (8080 unless you set another), then exactly the recorded units are enabled and started again. Apache and Allmon3 are also recorded in the list in Settings, System, ASL web services, so `apt purge nodespot` turns them back on; the other units are only restored by the undo command.

#### ASL3 telemetry

The installer no longer touches ASL3 (`NODESPOT_ASL_TELEMETRY` is gone). On a card that still has ASL3, turn its daily usage report off by hand:

```sh
sudo systemctl disable --now asl-telemetry.timer
sudo systemctl mask asl-telemetry.timer asl-telemetry.service
```

### ASL services (Cockpit, Apache, Allmon3)

**Settings, System, ASL web services** has one switch each for Cockpit, Apache and Allmon3. A switch only stops and disables the service (`systemctl disable --now`); nothing is uninstalled, so the ASL3 Pi appliance package keeps working, and turning it on again is one click. nodespot asks first and says what you lose (Cockpit is a fallback terminal and WiFi settings if nodespot ever breaks: keep it until you are sure SSH works). Turning Apache off offers to turn Allmon3 off too, because Allmon3 answers on ports 16080 and 16700+ on every address even without Apache. The page lists which of ports 80, 443, 9090, 16080 and 16700+ are really listening, and can close or reopen the matching firewalld services. nodespot never touches these services during an install or update, unless you ask for it with `NODESPOT_TAKEOVER=1` (above). `apt purge nodespot` turns back on the services nodespot itself had turned off (and no others); `apt remove` leaves everything as it is.

### Firewall

If firewalld is running, the package and the installer open the configured port for you: the firewalld service `nodespot` for 8080, a plain TCP port for any other port. If you edit `listen` by hand, open the new port yourself (`sudo firewall-cmd --permanent --add-port=<port>/tcp && sudo firewall-cmd --reload`); a package upgrade also opens the configured port. With another firewall (ufw, nftables, a router), allow the TCP port from your LAN yourself. Do not expose the port to the internet without a reverse proxy that provides TLS.

## Digital modes

nodespot has its own built-in digital voice support, so a node can talk on digital networks. Supported modes:

- **BrandMeister** and **TGIF** (DMR), and **DMR - Other** for any other Homebrew master, including DMR+ (pick a master from the list or type its address).
- **YSF** (System Fusion) reflectors, **P25** and **NXDN** talkgroups. P25 and NXDN are tested against recorded traffic but not yet on air.
- **Echo test**: a local test with no network and no account. You hear yourself back a second after you unkey, which proves the audio path.

Enable digital modes in **Settings, Digital Modes** (or `sudo nodespot dv enable`, or `NODESPOT_DV=1` at install). Enter your callsign and DMR ID, then turn on and set up the networks you want on the **DV** page. `sudo nodespot dv status` shows the state and `sudo nodespot dv disable` stops the engine and keeps your settings. Enabling installs only the software AMBE vocoder. It needs a 64-bit OS (32-bit armhf is not supported).

On the **DV** page, the Digital Modes card shows the bridge node and its link, has a button for each enabled mode, a tune entry (a trailing `#` makes a private DMR call), favorites and an **Unlink** button, and the **Modes** panel opens each network's switch and settings. The **Dashboard** has a DV tile with the same **Unlink** button. **Now calling** (a red strip on every page) shows who is talking, with callsign, name, place, network, talkgroup and timeslot, and **Last heard** lists recent calls. The engine resumes in the last mode after a restart. More in [docs/DV.md](docs/DV.md).

![DV page, Digital Modes card](docs/images/dashboard-digital.png)

![DV page, BrandMeister network panel](docs/images/digital-networks.png)

![Settings, Digital Modes](docs/images/digital-setup.png)

## Updating

Either of:

- **Settings, System, Updates** in the web page (administrators): *Check now*, then *Update*. Links are not affected; the page reconnects by itself.
- `sudo apt update && sudo apt upgrade`

## Terminal

**Settings, System, Terminal** (administrators, off by default): a Linux login prompt in the browser, using xterm.js. Turn it on in that section, then *Connect* and log in with a Linux account name and password; no root shell is handed out. It closes after 15 minutes without typing, two sessions at most; the audit log records when it was used, never what was typed.

## WiFi

**Settings, System, WiFi** (administrators): scan, add a network (with or without a password), remove one, reorder the saved networks, connect now. If the node has no network for 3 minutes and no Ethernet cable, it opens an unprotected setup network called `nodespot-<hostname>`; join it and open `http://10.42.0.1:<port>`. It switches off when a saved network connects. Use it only to set up WiFi, because others nearby can see what you type. Administrators can turn it off there. It needs NetworkManager and a WiFi device.

## Uninstall

```sh
sudo apt remove nodespot     # keeps users and favorites in /var/lib/nodespot
sudo apt purge nodespot      # also deletes them
```

`apt purge` also deletes the config history (`/var/lib/nodespot-config-history`) and nodespot's digital modes state (`/var/lib/nodespot-dvstate`, `/var/lib/nodespot-dvstate-history`). Your Asterisk config stays.

## Reporting bugs

Please [open a bug report](https://github.com/w1jar/nodespot/issues/new?template=bug_report.yml). The quickest way is **Settings, About, Report a bug** in nodespot: it opens the form with your versions and hardware already filled in.

Add logs if you can, either of:

- the diagnostics file: **Settings, Maintenance, Diagnostics** (administrators). It lists versions, system and node state, configuration and the recent server log. IP addresses, passwords and secrets, email addresses and other stations' callsigns are removed automatically, but please read it before you attach it;
- `journalctl -u nodespot -n 100 --no-pager`

Never paste passwords or other private data into an issue. Feature ideas are welcome too: [request a feature](https://github.com/w1jar/nodespot/issues/new?template=feature_request.yml).

![Settings, About section](docs/images/settings-about.png)

## Security

Please report vulnerabilities privately, not in public issues. See [SECURITY.md](SECURITY.md).

## Acknowledgements

nodespot's dashboard and favorites design is inspired by [AllScan](https://github.com/davidgsd/AllScan) by David Gleason (NR9V). nodespot is an independent implementation and is not affiliated with or endorsed by the AllScan project.

## License

nodespot is free software, licensed under the GNU General Public License v3.0 or later. See [LICENSE](LICENSE).

## Development

nodespot is a Go server (`cmd/nodespot`, plus the root helper `cmd/nodespot-helper`) with a Svelte 5 + Vite + Tailwind web UI (`web/`) embedded in the binary. This repository holds the source, the releases and, on the `gh-pages` branch, the signed apt repository served at <https://w1jar.github.io/nodespot>.

Requirements: Go (version in `go.mod`), Node 22 and npm for the web UI, `make`. Building the packages also needs network access the first time (nfpm is fetched with `go run`).

```sh
make web         # build the web UI into internal/webui/dist
make build       # bin/nodespot for this machine
make test        # go test -race ./...
make demo        # simulated node, no Asterisk needed; login demo / demo
make debs        # dist/nodespot_<version>_{arm64,amd64}.deb
make print-version
cd web && npm run dev:mock   # UI dev server with a mock API
```

Layout: `cmd/` (binaries), `internal/` (packages: `astconf`, `ami`, `rpt`, `api`, `auth`, `store`, `updates`, `helper`, ...), `web/` (UI), `packaging/` (nfpm config, systemd units, `install.sh`, `publish-apt.sh`), `docs/` (design, API and handover notes), `tools/`.

Read [docs/HANDOVER.md](docs/HANDOVER.md) first, then [docs/DESIGN.md](docs/DESIGN.md) and [docs/API.md](docs/API.md). Planned work: [docs/PLAN-dev-builds.md](docs/PLAN-dev-builds.md) (developer builds, deferred).

### Releasing

Add a `## [X.Y.Z]` section to `CHANGELOG.md`, then push the tag `vX.Y.Z`. The Release workflow builds the three debs, publishes them to the `beta` suite of the apt repository (the `gh-pages` branch) and creates a GitHub pre-release. The **Promote** workflow (manual, input: version) moves the same packages to `stable`. Details in [docs/HANDOVER.md](docs/HANDOVER.md).
