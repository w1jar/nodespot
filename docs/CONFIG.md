# Configuration editing (2.0)

> Removed: this describes a feature of the archived aslcp 2.x that needed Asterisk. nodespot runs its own node; see [NATIVE_NODE.md](NATIVE_NODE.md). Kept for reference.

Configuration editing of `docs/DESIGN.md` (sections 4-6), released in 2.0.

| Slice | Content |
|---|---|
| **beta.1: foundation** | File browser, raw editor, validate + diff review, snapshot history (git), apply through the helper, targeted reload, health check, rollback, history view with restore, Asterisk restart. |
| beta.4: forms | Schema (`internal/schema`) and typed forms for `rpt.conf` node settings, `simpleusb`, `iax` `[general]`, `echolink`, registrations, `manager`. Secrets masked. Provenance from `astconf.Effective`. |
| beta.5: guided | Node renumber, radio interface change, setup wizard (DESIGN 4.3, 8). |

Everything below is beta.1.

## Principles

- Admin only, CSRF-protected, every apply/restore/reveal audited (no secret values in audit payloads).
- Lossless: the raw editor saves exactly the bytes the user typed; `astconf.Parse` must succeed (syntax check) but never rewrites the text.
- Optimistic concurrency: every write carries the sha256 of the version it was based on; the helper refuses (conflict) if the file changed on disk meanwhile (e.g. `asl3-menu`, manual `nano`).
- The server never writes `/etc/asterisk` and never reads the history repository; both go through the root helper. The helper validates every path itself and never trusts the server.

## Helper protocol extension

Until now requests carried no arguments. Config ops add one optional, strictly typed field; the other ops must reject a request that carries it.

```go
type Request struct {
    Op     string         `json:"op"`
    Config *ConfigRequest `json:"config,omitempty"` // config-* ops only
}
type ConfigRequest struct {
    Path    string       `json:"path,omitempty"`    // config-read
    Files   []ConfigFile `json:"files,omitempty"`   // config-apply
    Message string       `json:"message,omitempty"` // commit message (<= 200 chars, one line)
    Author  string       `json:"author,omitempty"`  // nodespot username (<= 64, [A-Za-z0-9._-])
    Commit  string       `json:"commit,omitempty"`  // config-show / config-restore: 7-40 hex
    Limit   int          `json:"limit,omitempty"`   // config-log (1-200)
    Offset  int          `json:"offset,omitempty"`
}
type ConfigFile struct {
    Path    string `json:"path"`
    Content string `json:"content"`
    Base    string `json:"base"` // sha256 hex of the version edited; "" = new file
}
```

Request size limit for config ops: 4 MiB (others stay small).

**Path rules (enforced in the helper):** relative to `/etc/asterisk`; matches `^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`; ends in `.conf`; at most 4 components; after `filepath.Join` + `EvalSymlinks` of the parent directory the result stays inside `/etc/asterisk` (realpath of the root); the target itself, if it exists, must be a regular file (not a symlink, not a device). New files only under `custom/` (directory must already exist). Per file <= 1 MiB, <= 50 files per apply, content must be valid UTF-8 without NUL.

| Op | Request | Result |
|---|---|---|
| `config-list` | - | `[{path, size, mtime, sha256, mode, owner, group}]` for every `*.conf` under `/etc/asterisk` (recursive, max depth 4, symlinks not followed, at most 500 entries) |
| `config-read` | `path` | `{path, content, sha256}` |
| `config-apply` | `files, message, author` | `{commit, before}`; see pipeline |
| `config-log` | `limit, offset` | `[{commit, date, author, message, files: [path]}]`, newest first |
| `config-show` | `commit` | `{commit, date, author, message, files: [{path, status: added\|modified\|deleted, diff}]}` (unified diff vs parent, 3 lines context, raw: masking is the server's job) |
| `config-restore` | `commit, author` | `{commit, before}`: writes the files as they were in `commit` (only files that differ; files that did not exist then are left alone and listed in `skipped`) |
| `asterisk-restart` | - | `{ok}`: `systemctl restart asterisk` |

**History repository:** `/var/lib/nodespot-config-history` (not under `/var/lib/nodespot`: that directory is the `nodespot` user's data dir and is writable by it, so a repository inside it could be tampered with by the unprivileged server), root:root 0700, a bare-style git dir used with `GIT_DIR=<repo> GIT_WORK_TREE=/etc/asterisk`, `core.fileMode=true`, local identity `nodespot <nodespot@localhost>` with `--author` from the request. Initialized on first use with an "Initial snapshot" commit of the current tree. `git` is called with a fixed argv (never through a shell), `-c` options to disable hooks/pager/gpg (`core.hooksPath=/dev/null`, `commit.gpgSign=false`), env `HOME=/nonexistent`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_TERMINAL_PROMPT=0`. The package gains `Depends: git`.

**config-apply pipeline** (one at a time, under a lock shared with restore and update-apply):

1. Validate all paths and sizes; read every target; compare sha256 with `Base` (missing file and `Base==""` is ok). Any mismatch: error `conflict` naming the file(s); nothing written.
2. Snapshot: `git add -A` + commit "Before: <message>" if the tree differs from HEAD (captures outside edits). `before` = HEAD after this step.
3. Write each file atomically: temp file in the same directory (name not ending in `.conf`), copy uid/gid/mode of the existing file (new file: `asterisk:asterisk 0640` if that user/group exists, else root 0644), fsync, rename.
4. On any write error: rewrite the already-written files from `before` the same way, return the error.
5. Commit "<message>" with the author; return its id.

Restore is the same pipeline with the file contents taken from `git show <commit>:<path>`, message "Restore <short id>: <original message>".

## Server

All under `/api/config`, admin only.

| Method / path | Body | Response |
|---|---|---|
| `GET /api/config/files` | | `[{path, size, mtime, sha256, known: bool, group: "Core"\|"Radio"\|...}]` (group/known from the DESIGN 4.4 table; unknown files under "Other") |
| `GET /api/config/file?path=` | | `{path, content, sha256}` |
| `POST /api/config/preview` | `{files: [{path, content, base}]}` | `{files: [{path, ok, error?: {line, message}, diff, changed, conflict?}], reload: [{command, reason}], restart: bool}`; `conflict` is set when the file changed on disk since `base`; `reload` is `[]`, never null |
| `POST /api/config/apply` | `{files: [{path, content, base}], message, reload: bool}` | `{commit, before, files: [path], restart: bool, reload: [{command, ok, output}] \| null, health: {ok, checks: [{name, ok, detail}]} \| null}` (`reload`/`health` are null when `reload` was false); 409 `{code: "conflict", files: [path]}`; 422 `{code: "syntax_error", issues: [{path, line, message}]}` |
| `GET /api/config/history?limit=&offset=` | | helper `config-log` |
| `GET /api/config/history/{commit}?reveal=1` | | helper `config-show`; values of sensitive keys masked unless `reveal=1` (audited) |
| `POST /api/config/history/{commit}/restore` | `{reload: bool}` | like apply, plus `skipped: [path]` (files that did not exist in that commit) |
| `POST /api/system/asterisk/restart` | | `{ok}`; shares its implementation (AMI `core restart gracefully` when connected, else the helper's `systemctl restart`) with the older `POST /api/system/restart-asterisk` |

**Preview** parses each file with `astconf.Parse` (error with line on failure), computes a unified diff against the current content (server-side, Go), and derives the reload plan from the changed files.

**Reload plan** (AMI `Command`, fixed strings only; union over changed files, de-duplicated):

| File | Command |
|---|---|
| `rpt.conf`, `custom/rpt/*` | `module reload app_rpt.so` (verify on hardware) |
| `iax.conf`, `custom/iax/*` | `iax2 reload` |
| `extensions.conf`, `custom/extensions/*` | `dialplan reload` |
| `manager.conf` | `manager reload` (then the server reconnects AMI if its own credentials changed) |
| `simpleusb.conf` | `module reload chan_simpleusb.so` |
| `usbradio.conf` | `module reload chan_usbradio.so` |
| `echolink.conf` | `module reload chan_echolink.so` |
| `rpt_http_registrations.conf` | `module reload res_rpt_http_registrations.so` |
| `logger.conf` | `logger reload` |
| anything else | `restart: true` (the UI offers "Restart Asterisk" instead of reloading) |

The table lives in one Go map so it can be corrected after testing on the node (DESIGN 13, "Reload semantics").

**Health check** (after reload or restart, wait up to 15 s): AMI connected and answering; every local node that existed before the apply answers `rpt` status. Result per check. If not ok, the UI offers one-click rollback = restore of `before` + the same reload + health check.

**Masking:** keys `secret`, `password`, `passwd`, `pass`, `md5secret`, `dbsecret`, and `register =>` / `register =` values (the `user:secret@` part) are replaced with `********` in history diffs unless revealed. The raw editor shows real content (admin only); reading a file is audited as `config.read`.

## UI

New top-level "Config" page (admin only, hidden for others):

- Left: file list grouped (Core, Radio, Linking, Platform, Custom, Other), filter box. Right: editor (CodeMirror 6, line numbers, simple Asterisk highlighting for `[section]`, `key = value`, `;` comments, `#include`), dirty marker, Revert.
- Save opens a review dialog: per-file diff, syntax errors (block apply), reload plan or "needs Asterisk restart", commit message (default "Edit <file>"), "Reload after apply" checkbox (on). Apply shows progress, then reload results and health checks; failed health shows "Roll back".
- Conflict (409): explain the file changed on disk, offer reload-from-disk (the user's text is kept in a side buffer to copy from).
- History tab: commits (date, author, message, files), click for diffs with a Reveal secrets button, Restore button (same review/result flow).
- "Restart Asterisk" button with confirmation (warns that links drop).
- Demo mode and the mock API provide an in-memory `/etc/asterisk` with a few anonymized files.

## Test plan

- Helper: path validation table tests (traversal, absolute, symlink escape, non-.conf, depth, NUL, size), apply/conflict/rollback-on-write-error, ownership and mode preserved, restore, log/show, hooks disabled. Use a temp dir as root and a temp history repo.
- Server: preview diff and reload plan, masking, authorization (viewer/operator get 403), audit entries without secrets.
- On real hardware before beta.1: check `/etc/asterisk` ownership and modes, the reload commands in the table, and the health check after each.
