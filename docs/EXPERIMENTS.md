# Experiments

Ideas being tried on `try/*` branches (see "Experiments" in HANDOVER.md for
how test builds work). Nothing here is released. Keep one section per branch;
remove it when the branch is merged or dropped.

## try/i18n: UI translations (parked 2026-11-17)

**Status:** parked by the maintainer before testing on hardware. Branch
`try/i18n`, commit `9f33ba5`, based on `main` after 1.1.19. CI passed
(run 43993265049); its test build `1.1.19+dev.2.g9f33ba5` artifact expires
after 14 days. Push any commit to the branch to get a fresh build.

**What it does**
- Dependency-free i18n layer in `web/src/lib/i18n/`: `t(key, params)` with
  `{name}` interpolation and plurals via `Intl.PluralRules` (Russian
  one/few/many). Lookup falls back locale → en-US → key.
- `en-US.ts` is the source dictionary; the key type is derived from it, so a
  misspelled key fails `npm run check`. `ru.ts` is typed complete (727 keys);
  `en-GB.ts` is a partial override (favourite, colour, behaviour, …).
- Locale: saved per browser (`localStorage` key `nodespot-locale`), else the best
  match of the browser languages, else en-US. Sets `<html lang>`.
- Dates, times, numbers and relative times follow the locale (`util.ts`).
- Language picker (`LanguageSelect.svelte`) in Settings → About, the header
  user menu, and the login, setup and public pages. Each language is shown in
  its own name.
- The CodeMirror find/replace panel is translated.
- Layout fixes for longer Russian text: header nav wraps on phones, wider mode
  column in the status tables (`:lang(ru)`), dashboard footer buttons wrap.
  English layout unchanged.
- How-to for adding strings and languages: `docs/I18N.md` (on the branch).

**Russian terminology chosen:** узел (node), связь (link; "линк" is the
alternative to ask about), передача / приём (TX / RX), позывной (callsign),
избранное (favorites), мониторинг (monitor).

**Not translated (by design):** server API error messages, log output, config
file contents, node numbers, callsigns, node descriptions, favorites group
names, product/tech names (nodespot, AllStarLink, Asterisk, AMI, EchoLink, apt,
systemd, file names), labels built from server keys (Tune and node-stats key
names), and the pre-filled "Edit …" commit message in the config apply dialog.

**Open before merging**
1. Maintainer review of the Russian on a real node (wording, truncation).
2. Dialogs not yet checked visually: config apply, backup restore, update and
   OS-update confirmations, user management dialogs.
3. Decide whether server error messages should be translated (e.g. map the
   API `code` field to dictionary keys on the client).
4. Rebase or merge `main` into the branch first: it touches nearly every
   component, so expect conflicts with UI changes made on `main` since
   `9f33ba5`. Re-run `npm run check` afterwards: new strings added on `main`
   must get keys (the typed Russian dictionary will flag missing ones).
5. CHANGELOG entry when it merges.

**Resume:** `git fetch origin try/i18n && git checkout try/i18n`, merge
`main`, push; install the new CI build on the node:
`sudo apt install ./nodespot_<version>_<arch>.deb`; go back with
`sudo apt install --allow-downgrades nodespot=<release>`.

## After 2.0

Ideas for after the 2.0 line, not started.

- Split nodespot-dv into its own repository: a thought experiment for now. First step: make nodespot-dv run on its own inside this repo (own config, documented and versioned control protocol, own .deb), then move it once the boundary is stable.
