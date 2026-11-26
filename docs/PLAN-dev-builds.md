# Plan: developer builds from the Updates page

Status: **Planned, not started (deferred)**. Nothing here is built.

## 1. Goal

Admins install CI builds of any `try/*` branch on a node from System > Updates. The page lists the last 10 builds with CI run # (linked), short commit, build number, branch, date and status, and marks the installed one.

## 2. Rejected options

- **CI artifacts:** downloading them needs a GitHub token even for a public repo, they are zips, and they expire after 90 days. A token on the node is not acceptable.
- **gh-pages for the debs:** debs would bloat that branch's history forever.

## 3. Hosting

- The debs go on one rolling GitHub pre-release, `dev-builds`. It keeps the assets of the 10 newest builds and deletes older ones. Assets on a public repo download anonymously.
- A small `dev.json` index goes on the `gh-pages` branch next to `latest.json` and `latest-beta.json`, served by GitHub Pages at the `[updates]` url.
- Index fields: run number and URL, commit SHA, build number (the `+dev.N` count), branch, date, package version, and per arch (amd64, arm64, armhf) the asset URL, size and sha256.

## 4. CI

A new publish job in `ci.yml`:

- runs after the tests pass, only on a push to `try/**` in `w1jar/nodespot`;
- has a concurrency group, so builds publish one at a time;
- is the only job with `contents: write`;
- uploads the assets, prunes to the 10 newest builds, and updates `dev.json` on `gh-pages`. Reading `dev.json` needs a cache buster, as for `latest.json` (the CDN holds it about 10 minutes).

### Open question: the tag

A release needs a tag, so CI would create and move a non-version tag `dev-builds`. It does not match `v[0-9]+.[0-9]+.[0-9]+`, so `release.yml`, `promote.yml` and `release-delete.yml` ignore it, and as a pre-release it is never "latest". **The owner has not yet approved CI creating this tag.**

## 5. Node side

- A new helper op downloads the deb for `dpkg --print-architecture` over HTTPS into a root-only cache and verifies the sha256.
- It installs with `apt-get install --allow-downgrades ./file.deb` in a transient systemd unit (like the existing updates apply), so the install survives the nodespot restart.
- Dev versions are `X.Y.Z+dev.N.gSHA` counted from the last tag, so they sort below newer releases. The UI warns about the downgrade.
- A "Back to the release" button reinstalls the channel's current version from apt (with allow-downgrades).
- A build without a deb for the node's arch is listed but cannot be installed.

## 6. Access and safety

- Admin only.
- Hidden behind a "Developer builds" switch that is off by default and stored server-side.
- Needs a confirmation.
- Writes audit rows: `updates.dev.install` (run #, commit and version) and `updates.dev.revert`.
- 409 when apt is busy or an update is running; 503 without a helper.

## 7. Proposed endpoints

| Method and path | Purpose |
|---|---|
| `GET /api/updates/dev` | The switch state and the list of builds (the last 10), with the installed one marked. |
| `PUT /api/updates/dev/enabled` | Turn the "Developer builds" switch on or off. |
| `POST /api/updates/dev/install` | Body `{run}`: install the build of that CI run. |
| `POST /api/updates/dev/revert` | Back to the channel's current release. |

## 8. Planned version

A new `try/1.3.5` off `try/1.3-stack` (which becomes `main` after 1.3.4).

## 9. Still open

- Approval of the `dev-builds` tag by the owner.
- Whether to sign `dev.json`.
- Verification on a node.
