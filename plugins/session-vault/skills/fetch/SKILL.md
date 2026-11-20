---
description: List the user's saved Claude Code sessions, or restore one into the current project by id or name
allowed-tools: [Bash]
argument-hint: "[<session-id-or-name> | all]"
---

Fetch the user's saved sessions from their session-vault server, using
`"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh"` with Bash.

Map the user's arguments (`$ARGUMENTS`) to a subcommand:

- **No arguments** — run `... list` and show the table. It includes each
  session's NAME (if the user labeled it at save time), SESSION ID, and
  ORIGINAL CWD so the user can choose what to restore; tell them to run
  `/session-vault:fetch <name-or-id>` to restore one here.
- **A session id or name** — run `... get <value>`. This restores the
  session into the **current** project directory (remapping it from whatever
  machine/OS/path it was saved on) so it is immediately resumable from here.
  Relay the script's `claude --resume <id>` hint and any note about the
  original path verbatim.
- **`all`** — run `... all`, which mirrors every missing session back to its
  original project directory (backup restore, no remapping).

An existing local transcript is only replaced when the server holds a
newer save of it; the CLI then keeps the previous copy as a `.bak` and
says so — relay that line. If configuration is
missing, point the user to `~/.config/session-vault/config` (needs
`SESSION_VAULT_URL` and `SESSION_VAULT_TOKEN`). If a restore fails because
the session is encrypted and no key is present, tell the user to copy
`~/.config/session-vault/age.key` from the machine where it was generated.
