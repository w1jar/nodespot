# Handover notes

Read this first in a new session, then `docs/DESIGN.md`, `docs/API.md` and `CHANGELOG.md`.

## What nodespot is

All-in-one multi-mode radio node for Raspberry Pi and mini PCs (Debian 12 or 13 / Raspberry Pi OS, arm64/amd64): the native node (`nodespot-node`, one radio, one active mode: AllStar, EchoLink, DMR, YSF, P25, NXDN, M17) and the web control panel. It continues the archived aslcp project (https://github.com/w1jar/aslcp, final release 2.4); nothing is migrated from it and there is no upgrade path (install fresh). See `docs/NATIVE_NODE.md`.

## Repository (single public repo)

- **`w1jar/nodespot`** holds everything: the source on `main` (README, LICENSE, SECURITY.md, issue forms in `.github/ISSUE_TEMPLATE`, screenshots in `docs/images`), the GitHub releases (created from the `vX.Y.Z` tags) and the signed apt repository on the **`gh-pages`** branch (`pool/`, `dists/`, `latest.json`, `latest-beta.json`, `install.sh`, keyring files, `index.html`). GitHub Pages serves `gh-pages` (root) at https://w1jar.github.io/nodespot, so the apt URL is `APT_URL` in the Makefile and workflows.
- `gh-pages` is written only by the workflows (`release.yml`, `promote.yml`, `apt-remove.yml`) through `packaging/apt-checkout.sh` and `packaging/publish-apt.sh`, using the built-in `github.token` (`permissions: contents: write`); there is no personal access token. Never edit or push `gh-pages` by hand.
- Development sessions may be configured with a `claude/*` branch; the maintainer has said to push directly to `main`. Check `git remote get-url origin` before any git network command.
- Because the repository is public, never commit secrets, real IPs or other operators' callsigns.

### One-time setup (maintainer)

1. Create the public repository `w1jar/nodespot` (empty) and push the source to `main`.
2. Add the repository secret `APT_SIGNING_KEY` (Settings > Secrets and variables > Actions): the armored private GPG key that signs the apt repository (uid "nodespot apt repository", RSA 4096, no passphrase). The key was generated fresh for nodespot (it is not the aslcp key); the public key is published as `nodespot-archive-keyring.gpg` on the Pages site. GitHub secrets cannot be read back; the maintainer keeps a copy of the key.
3. Settings > Actions > General > Workflow permissions: the workflows request `contents: write` themselves, but "Read and write permissions" must not be blocked by an organization policy.
4. Push the first tag `v1.0.0`. The Release workflow creates the orphan `gh-pages` branch on this first run (and the pre-release).
5. After that first release: Settings > Pages > Build and deployment > Source "Deploy from a branch", branch `gh-pages`, folder `/ (root)`. Then check https://w1jar.github.io/nodespot/ and `install.sh`. (Until the branch exists it cannot be selected.)

## Releasing

Versions are plain `X.Y.Z`; the first release is 1.0.0. Every tag is first a beta; a release is a separate **promote** step. A repository without tags builds `0.0.0+dev.N.g<sha>` packages (N = commits on HEAD), which sort below 1.0.0.

1. Add a `## [X.Y.Z]` section to `CHANGELOG.md` on the branch being released (the release notes come from it), push to `main`, wait for CI.
2. The maintainer pushes the tag `vX.Y.Z` (or creates it on https://github.com/w1jar/nodespot/releases/new, target `main`, "pre-release" ticked). Tag pushes from development sessions are blocked; do not work around that (the maintainer chose to keep the final say). Only tags of the form `vX.Y.Z` trigger the build.
3. `.github/workflows/release.yml` (taken from the tagged commit) tests and builds the arm64 and amd64 debs, then `packaging/publish-apt.sh` puts them in `pool/beta`, writes `latest-beta.json`, signs (secret `APT_SIGNING_KEY`) and pushes to the `gh-pages` branch of this repository (built-in token); the debs are attached to a GitHub pre-release. Suite `beta` indexes all of `pool/`, suite `stable` only `pool/main`. `install.sh` is refreshed only when the published version is the newest in the pool. armhf is no longer built; the armhf debs published earlier stay in the pool and stay indexed (frozen: never pruned, nothing added). The newest 5 builds per architecture are kept in each pool directory (the version just added is never pruned).
4. After testing on a node (Updates > Beta), the maintainer runs the **Promote** workflow (Actions > Promote > Run workflow, input `version`, e.g. `1.0.0`, no leading v). It moves exactly those debs from `pool/beta` to `pool/main` (no rebuild), regenerates and signs both suites, updates `latest.json`, and marks the GitHub release as a full release (and "latest" if it is the newest stable version). It refuses a version that is not in the beta suite and is safe to run twice. If the release has deb assets they must be identical to the packages in the apt repo.
5. Nodes update from the Updates tab or `apt upgrade`; the channel (Release/Beta on the Updates tab) is the suite word in `/etc/apt/sources.list.d/nodespot.list`, switched by the helper ops `update-channel-stable|beta`.
6. To delete unwanted beta packages: the **Remove from beta** workflow (`.github/workflows/apt-remove.yml`; inputs `glob`, `keep_version`, `apply`; dry run unless `apply` is ticked; it only touches the beta pool and refuses if `keep_version` would be gone). It runs `publish-apt.sh --remove`.
7. To delete old GitHub releases (for example old pre-releases whose tags are already gone): the **Delete releases** workflow (`.github/workflows/release-delete.yml`; inputs `glob`, `delete_tags`, `apply`; dry run unless `apply` is ticked). It deletes matching pre-releases by release id (never full releases); with `delete_tags` also the matching git tags, except a full release's tag. It does not touch the apt repository.

## Current state

- No release has been tagged yet; the first one is 1.0.0 (the CHANGELOG section `## [1.0.0]` holds its notes). Everything is untested on hardware until the maintainer installs a beta build on a node.

## Working agreements

- `CLAUDE.md`: launch subagents for each task and use the cheapest adequate model. Use Haiku for small, well-defined jobs (doc edits, small fixes, version bumps) and Sonnet for multi-file features; review security-relevant work carefully, lighter review for low-risk changes.
- The repository is public. Bug reports go to its issues; security reports via GitHub private vulnerability reporting.
- Never commit real secrets, IPs, node numbers or other operators' callsigns; use placeholders (N0CALL, node numbers 1997 to 2002, 192.0.2.x).
- Design preferences from the maintainer: dark theme default; AllScan-like density (status table at top with red = TX / keyed link, green = local RX, then the connect bar with "Disconnect before Connect" on and "Disconnect before Monitor" off, then a favorites table; network list always open); no PHP; easy install/update; one port with public view + login.

## Layout cheat sheet

- `cmd/nodespot` (server, `nodespot admin ...` CLI), `cmd/nodespot-helper` (root helper, fixed ops over a unix socket).
- `cmd/nodespot-node` (the native node daemon, `internal/native/...`), `internal/`: `api`, `auth`, `store` (SQLite), `backup`, `updates`, `helper`, `debver`, `diag`, `schedules` (`docs/SCHEDULES.md`), `webui` (embedded UI). `nodespot --demo` runs a simulated node.
- `web/` (Svelte 5 + Vite + Tailwind; `npm run dev:mock` for a mock API), `packaging/` (nfpm, systemd units, postinst, install.sh, publish-apt.sh), `tools/capture-ami.sh`.
- Commands: `make web`, `make build`, `make debs`, `make test`, `make demo`.

## Experiments (try/* branches)

To try an idea on one node without a release: push it to a branch `try/<name>`. CI builds the debs and attaches them to the run as the artifact `debs-try-<name>-<version>` (kept 14 days); nothing goes to the apt repository or GitHub releases. Untagged builds are versioned `X.Y.Z+dev.N.g<sha>` (N commits after the last release tag `vX.Y.Z`), or `0.0.0+dev.N.g<sha>` while there is no tag at all; both sort below the next release, so it installs over them. Install on the node with `sudo apt install ./nodespot_<version>_<arch>.deb`; go back with `sudo apt install --allow-downgrades nodespot=<release>`. If an experiment changes the database or config format, say so: going back may need care. Keep or drop: merge the branch to `main` (it then ships in the next release) or delete it.

Parked and running experiments, with their status and how to resume: `docs/EXPERIMENTS.md`.
