# Multi-node view (parked)

> Removed: this describes a feature of the archived aslcp 2.x that needed Asterisk. nodespot runs its own node; see [NATIVE_NODE.md](NATIVE_NODE.md). Kept for reference.

Status: **parked**. Not scheduled. The owner decided to skip it for 1.3.x and revisit later. Nothing here is built.

Node numbers below are placeholders: nodes 2001, 2002 and 2003, host `node2001`, address `192.0.2.10`.

## 1. Why

Large networks (ECR is one) and hub operators publish one public page that shows several of their nodes. Usually this is Supermon's `link.php?nodes=2001,2002,2003`. nodespot shows only the local node.

## 2. What the existing tools do

Research done in Nov 2026. Part of it is **unconfirmed**. The ASL3 docs site (allstarlink.github.io) was not reachable during research, so some points come from memory or search snippets.

### Allmon3

- Each node is a section in `/etc/allmon3/allmon3.ini`. It has host, port, user and pass: the Asterisk manager (AMI) of that node, local or remote.
- `multinodes = ...` lists several nodes on one host.
- A logged-in user can command any listed node: connect, disconnect, commands menu, disconnect-all.
- `/etc/allmon3/user-restrictions` sets per-node limits. A user not listed there can command all nodes.
- It has a voter display and remote pollers.
- **Verified** on a node: allmon3 only Recommends apache2. It serves its own UI on 16080 and websockets from 16700, on all addresses by default.
- Anonymous read-only view: likely, **unconfirmed**.

### Supermon (classic Supermon2 and Supermon-ng)

- `link.php?nodes=...` shows one table per node: connected nodes, last received, link type, direction, connected time, mode (transceive or monitor) and keyed state.
- It is live through server-sent events from `server.php`.
- Every listed node must be in the Supermon host's `allmon.ini` with an AMI login.
- Anonymous visitors can only watch.
- Control needs a login: connect, disconnect, monitor, local monitor, permanent, DTMF, reload, restart Asterisk, reboot.
- `authusers.inc` and `authini.inc` give per-user buttons and node lists. They ship disabled.
- Groups can put several nodes on one page. Syntax **unconfirmed**.
- The Supermon-ng feature set is **unconfirmed**.

### stats.allstarlink.org

It already shows status and links for nodes that opt in (`statpost_url` in `rpt.conf`).

### Evidence

Public multi-node pages are display-only. No evidence was found of operators controlling many remote nodes from one dashboard. This is only absence of evidence.

## 3. Risk of copying their model

Remote control needs each node's AMI on TCP 5038. AMI logins and commands are plaintext. So it needs firewall holes or a VPN. It also needs per-user, per-node permissions. That is a lot of attack surface for a feature few operators seem to use.

## 4. Proposed design if revived

A read-only multi-node page. nodespot no longer has a public (logged-out) status page, so this would need a new read-only endpoint on each peer, behind its own opt-in setting.

- The admin lists peer nodes that run nodespot, one URL per peer, for example `https://node2001.example/`.
- The page gets each peer's own public status over HTTP(S).
- Local nodes on the same host come from the local Asterisk, as today.
- No AMI over the network. No remote control.
- Each peer decides what it publishes through its own opt-in setting.
- Layout: one card or table per node, like Supermon's link page.

### Fetch options

| | Browser fetches peers (CORS) | Local nodespot fetches (server-side) |
|---|---|---|
| CORS | Each peer must send CORS headers. | Not needed. |
| Viewer IP | Seen by every peer. | Hidden from peers. |
| Local node role | Unchanged. | Becomes a fetcher: timeouts, caching, retries. |
| SSRF | Not a local risk. | Needs a guard: only configured peers, no redirects to other hosts, no private ranges unless the admin listed them. |
| Mixed content | An https page cannot call http peers. | No problem. |

The leaning is server-side, because of CORS and viewer privacy. It is not decided.

## 5. Open questions

- Server-side or browser fetch?
- Peers that do not run nodespot: use stats.allstarlink.org as a fallback source?
- Refresh rate and caching. SSE per peer, or polling?
