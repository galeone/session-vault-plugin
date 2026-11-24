# Session Vault

Back up your Claude Code sessions and use them on any machine. Claude Code
keeps each conversation in `~/.claude`, only on the machine where it
happened, and deletes old transcripts after a while. This plugin saves the
sessions you want to keep to a Session Vault server, encrypted on your
machine first, and restores them on any of your machines so you can carry
on where you left off.

The server can be the hosted service at
[session-vault.dev](https://session-vault.dev) (free plan: 10 sessions) or
your own, self-hosted from
[galeone/session-vault-server](https://github.com/galeone/session-vault-server).

## Commands

| Command | What it does |
|---|---|
| `/session-vault:save [name]` | Uploads the current session, optionally under a name |
| `/session-vault:list` | Lists your saved sessions |
| `/session-vault:fetch [name-or-id \| all]` | Restores a session into the current project (or all of them) |
| `/session-vault:clean <name-or-id>` | Deletes a saved session from the server, after asking |

## Examples

- "Save this session as `auth-refactor`" runs `/session-vault:save
  auth-refactor`. On your other laptop, open the same repository and run
  `/session-vault:fetch auth-refactor`, then resume it with `claude
  --resume`.
- "What sessions do I have saved?" runs `/session-vault:list` and shows each
  session's name, id and the folder it was saved from.
- "Restore everything I saved" on a new machine runs `/session-vault:fetch
  all`, which puts every saved session back in its original project.
- "Delete the session called `old-spike`" runs `/session-vault:clean
  old-spike`, which asks you to confirm first.

## Setup

Install it in Claude Code:

```
/plugin marketplace add galeone/session-vault-plugin
/plugin install session-vault@galeone-plugins
```

Then:

1. Get a server URL and a token: sign up at
   [session-vault.dev](https://session-vault.dev), or run your own server.
2. Create `~/.config/session-vault/config`:

   ```sh
   SESSION_VAULT_URL=https://session-vault.dev
   SESSION_VAULT_TOKEN=your-token
   ```

3. Run `/session-vault:save`. The first save creates your encryption key in
   `~/.config/session-vault/age.key`. Copy that file to every machine you
   want to fetch on, and keep a backup: nobody can decrypt your sessions
   without it, the server operator included.

## What it runs, sends and fetches

- **Runs**: `scripts/session-vault.sh`, which starts the `session-vault`
  client. If the client isn't installed (`$SESSION_VAULT_BIN` or
  `session-vault` on `PATH`), the script downloads the prebuilt binary for
  your platform once from this repository's
  [GitHub releases](https://github.com/galeone/session-vault-plugin/releases)
  into `~/.local/share/session-vault/bin/`, and runs it only if its SHA-256
  matches the checksum pinned in the script. The binaries are built by
  GitHub Actions from the Go source in [`cli/`](https://github.com/galeone/session-vault-plugin/tree/main/cli),
  which you can also build yourself.
- **A `SessionStart` hook** runs `session-vault track`, which writes the
  session id, transcript path and working directory to
  `~/.local/state/session-vault/by-cwd/` so that `save` knows which
  transcript is the current one. It sends nothing over the network.
- **Sends**, only when you run a command, and only to the server in your
  config: the transcript, gzip-compressed and encrypted with
  [age](https://age-encryption.org) on your machine (the server can't read
  it); next to it, in clear, the session id, the project name, the
  directory the session was saved from and the optional name you give it,
  which the server needs to list and restore your sessions; and your token,
  to authenticate.
- **Fetches**: your own saved sessions from that same server, and the
  client binary from GitHub as described above. No analytics, no telemetry,
  nothing sent anywhere else.
- **Stores locally**: your config and encryption key in
  `~/.config/session-vault/`, restored transcripts in `~/.claude/projects/`,
  logs in `~/.local/state/session-vault/logs/` (deleted after 21 days).
  Restored transcripts are plain text, like the ones Claude Code writes,
  and can contain secrets you pasted or that commands printed (tokens,
  `.env` contents): restoring a session copies those to the machine you
  fetch on, and the CLI prints a warning when it does.

## Privacy and support

- Hosted service: the [privacy policy](https://session-vault.dev/privacy)
  and [terms](https://session-vault.dev/terms) of session-vault.dev. A
  self-hosted server keeps everything on your own infrastructure.
- Support: [GitHub issues](https://github.com/galeone/session-vault-plugin/issues)
  or [support@session-vault.dev](mailto:support@session-vault.dev).
- Troubleshooting: the client logs to
  `~/.local/state/session-vault/logs/`; set `SESSION_VAULT_LOG_LEVEL=debug`
  for more detail.

## License

MIT, see [LICENSE](LICENSE).
