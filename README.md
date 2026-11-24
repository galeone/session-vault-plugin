# session-vault (Claude Code plugin)

Save your Claude Code sessions to your own server with `/session-vault:save`, and get them
back on any machine with `/session-vault:fetch`. End-to-end encrypted with
[age](https://age-encryption.org): the server only ever stores ciphertext.

This repository is a Claude Code **plugin marketplace** containing a single
plugin, `session-vault`, plus the Go source of its client CLI (`cli/`). The
companion server lives in the [`session-vault-server`](../session-vault-server)
repository.

Works on Linux, macOS, and native Windows (Git Bash): the plugin is a thin
wrapper around a single static Go binary, downloaded automatically on first
use — no runtime dependencies to install.

## Install

Inside Claude Code:

```
/plugin marketplace add galeone/session-vault-plugin
/plugin install session-vault@galeone-plugins
```

(For local development: `/plugin marketplace add /path/to/session-vault-plugin`.)

The plugin is also submitted to Anthropic's plugin directory, so it can be
discovered and installed from the `/plugin` directory listing once approved.

## Configure

Create `~/.config/session-vault/config`:

```sh
SESSION_VAULT_URL=https://sessions.example.com
SESSION_VAULT_TOKEN=your-token
# Optional, defaults to ~/.config/session-vault/age.key:
# SESSION_VAULT_AGE_KEY=/path/to/age.key
```

All values can also be provided as environment variables, which take
precedence over the file.

The CLI logs structured text (slog) to stderr and to a daily file named
`YYYY-MM-DD.log` under `$SESSION_VAULT_LOG_DIR`, or
`$XDG_STATE_HOME/session-vault/logs`, or `~/.local/state/session-vault/logs`
(logs older than 21 days are deleted). `SESSION_VAULT_LOG_LEVEL=debug|info|
warn|error` (default `info`) sets the level; command output on stdout is
never affected.

### End-to-end encryption

Every upload is end-to-end encrypted, always: on the first `/session-vault:save`
the client generates an age identity for you (the binary has age built in,
no separate install needed) and stores it at
`~/.config/session-vault/age.key`. You can also create it ahead of time with
`session-vault keygen`. `/session-vault:save` uploads
`age(gzip(transcript))` and the server only stores ciphertext it cannot read
(it refuses anything else);
`/session-vault:fetch` decrypts locally with the same key. Copy `age.key` to
every machine that needs to fetch your sessions — **if you lose the key,
your sessions are unrecoverable**. The key file is compatible with the
standalone `age` CLI.

Restores refuse sessions the server reports as unencrypted: a server that
cannot read your sessions also must not be able to plant one you would
then resume. Session ids and project names the server returns are validated before
they are used as file names.

### What it runs, sends and fetches

- **Runs:** one bash script and the `session-vault` CLI (downloaded from
  this repo's GitHub releases when missing, and run only if its SHA-256
  matches the checksum pinned in the script); age encryption is built into
  that binary. Nothing else.
- **Sends:** to the server URL you configured, and only there: the
  encrypted transcript (`age(gzip(…))` ciphertext), plus in clear the
  session id, project name, original working directory and optional name
  the server needs to list and restore it, and your token. No analytics,
  no telemetry.
- **Fetches:** your own saved sessions from that same server.
- **Reads locally:** transcripts under `~/.claude` and your own key at
  `~/.config/session-vault/age.key`.

## Usage

| Command | What it does |
|---|---|
| `/session-vault:save` | Uploads the current session's transcript to the server |
| `/session-vault:save <name>` | Same, labeled with a memorable name for later restore |
| `/session-vault:list` | Lists saved sessions (name, id, original cwd) |
| `/session-vault:fetch` | Lists saved sessions to choose from, or restore one (`<name-or-id>`) / mirror all (`all`) |
| `/session-vault:fetch <name-or-id>` | Restores that session **into the current project**, ready to resume here |
| `/session-vault:fetch all` | Mirrors every missing session back to its original project directory |
| `/session-vault:clean <name-or-id>` | Deletes a session from the server (asks for confirmation first; local files are untouched) |
| `/session-vault:rotate-key` | Re-encrypts every saved session to a new age key and replaces your key (asks for confirmation first; copy the new key to your other machines afterwards) |
| `/session-vault:autosave [on\|off]` | Shows or changes automatic saving: on by default, every session is saved in the background when it ends |

The core workflow this enables: work on Linux in `/data/you/repo`,
`/session-vault:save my-feature`, then on any other machine — different OS,
different clone path — open the repo and run `/session-vault:fetch my-feature`.
The transcript is remapped into
the current directory's project, and `claude --resume <id>` continues the
session there. (The session's *history* still references paths from the
original machine; the CLI prints a note reminding you of where it was
recorded.)

Restores keep an existing local transcript unless the server holds a
newer save of it (e.g. you continued the session on another machine and
saved it there); the replaced local copy is kept next to it as
`<id>.jsonl.<timestamp>.bak`. Local progress made after your last fetch or
save is never replaced. Re-saving a labeled
session without repeating the name keeps its label. When you save without a
name, the CLI uses the session title Claude Code generates (the one shown in
the terminal title) — e.g. `Apple access from Teams thread` becomes
`apple-access-from-teams-thread` — so every save gets a recognizable label
automatically.

## How it works

- Claude Code stores transcripts locally at
  `~/.claude/projects/<cwd-slug>/<session-id>.jsonl`, where `<cwd-slug>` is
  the absolute working directory with every non-alphanumeric character
  replaced by `-` (`CLAUDE_CONFIG_DIR` is honored).
- Slash commands cannot see the current session id directly, so the plugin
  ships a `SessionStart` hook that runs `session-vault track`, recording
  `{session_id, transcript_path, cwd}` to
  `~/.local/state/session-vault/by-cwd/<cwd-slug>.json`.
- `/session-vault:save` passes Claude Code's own session id
  (`${CLAUDE_SESSION_ID}`) to the CLI, which finds that session's transcript
  through the recorded state or by searching the project directories, so
  it works even after the shell has changed directory. Without an id
  (older Claude Code) it reads the state for the current directory
  (falling back to the most recently modified transcript for the project) and uploads it gzip-compressed and
  age-encrypted (a key is generated on the first save if none exists).
- A `SessionEnd` hook runs `session-vault session-end`. Claude Code gives
  all SessionEnd hooks 1.5 seconds together, which a plugin can't raise,
  so the hook only starts `session-vault autosave-run <id>` as a detached
  process (its own session on Unix, a detached process on Windows) and
  returns. That process waits 2 seconds for Claude Code to finish writing,
  then saves the session unless it has no assistant reply, hasn't changed
  since the server's newest save of it, or there is no age key yet.
  `SESSION_VAULT_AUTOSAVE=off` (config file or environment) turns it off.
  When a save is refused with 402 (plan limit reached), the background
  process records it in `~/.local/state/session-vault/`, and the next
  `SessionStart` hook prints it once as `{"systemMessage": ...}`, which
  Claude Code shows to the user.
- Subagents (Task/Agent tool calls) get their own transcripts in
  `~/.claude/projects/<cwd-slug>/<session-id>/subagents/`. When a session
  has any, `save` uploads them with the main transcript as one encrypted
  bundle (`X-Encrypted: age+gzip+tar`), and a restore puts them back next to
  the restored transcript. A session without subagents is uploaded as the
  plain transcript (`age+gzip`), as before. Restoring a bundle needs plugin
  1.7.0 or later (older clients refuse it and ask to update), and saving one
  needs session-vault-server 2.1.0 or later: with an older server, `save`
  uploads the main transcript only and says so.
- Transcripts are treated as opaque blobs: their JSONL format is internal to
  Claude Code and changes between versions, so nothing here parses them.

### The client binary

[`scripts/session-vault.sh`](plugins/session-vault/scripts/session-vault.sh)
resolves the CLI in this order:

1. `$SESSION_VAULT_BIN` (explicit override)
2. `session-vault` on `PATH`
3. a cached prebuilt binary in `~/.local/share/session-vault/bin/`,
   downloaded from this repo's GitHub releases on first use

The wrapper itself needs only `bash` and `curl`, which Claude Code already
requires on every platform (Git Bash provides both on Windows).

Build from source instead: `cd cli && go build -o session-vault .` and put
the binary on `PATH`. Cut release artifacts for all platforms with
`cli/build-release.sh <version>`.

## Security notes

- Transcripts can contain source code, secrets pasted into the chat, and
  command output. Uploads are always end-to-end encrypted (above), so the
  server only stores ciphertext; still, always use HTTPS.
- The token is sent as a `Bearer` header; keep the config file and age key
  private (`chmod 600`).

## Repository layout

```
.claude-plugin/marketplace.json      Marketplace catalog (name: galeone-plugins)
plugins/session-vault/
├── .claude-plugin/plugin.json       Plugin manifest
├── skills/{save,fetch,list,clean,rotate-key,autosave}/SKILL.md
├── icon.png                         Directory listing icon
├── hooks/hooks.json                 SessionStart (track) and SessionEnd (session-end) hooks
└── scripts/session-vault.sh         Binary bootstrap wrapper
cli/                                 Go source of the session-vault client
```

## CI & releases

- **CI** (`.github/workflows/ci.yml`) runs on every push to `main` and every PR:
  `go test` + `golangci-lint` on `cli/`, `bash -n` + `shellcheck` on the
  bootstrap script, and `jq` validation of the plugin JSON manifests.
- **Releases** are cut by [release-please](.github/workflows/release-please.yml)
  on push to `main`, following [Conventional Commits](https://www.conventionalcommits.org):
  - `fix: ...` → **patch** (0.2.0 → 0.2.1)
  - `feat: ...` → **minor** (0.2.0 → 0.3.0)
  - `feat!:` / `BREAKING CHANGE:` footer → **major** (0.2.0 → 1.0.0)
  - other types (`chore`, `docs`, `ci`, ...) only land in the next changelog
- When a release is cut, release-please creates the `vX.Y.Z` tag, `CHANGELOG.md`
  and an empty GitHub release. That tag triggers
  [release-artifacts](.github/workflows/release-artifacts.yml), which:
  1. cross-compiles the static client for linux/darwin/windows × amd64/arm64
     via `cli/build-release.sh` (version injected via `-ldflags -X main.version`),
  2. attaches the binaries + `checksums.txt` to the release (this is what the
     bootstrap script in `session-vault.sh` downloads and verifies), and
  3. keeps the in-repo version strings in `marketplace.json`, `plugin.json` and
     `scripts/session-vault.sh` in sync with the tag (pushed back to `main`).
