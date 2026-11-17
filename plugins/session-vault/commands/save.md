---
description: Save the current Claude Code session transcript to the configured session-vault server, optionally under a memorable name
allowed-tools: [Bash]
argument-hint: "[name]"
---

Upload the current session's transcript to the user's session-vault server.

Run this command with Bash and show the user its output. If the user provided
an argument, pass it as the session's name (letters, digits, `-`, `_` only —
slugify it if needed) so they can later restore with `/session-vault:fetch <name>`:

```
"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" save [name]
```

- If it fails because `SESSION_VAULT_URL` or `SESSION_VAULT_TOKEN` is not
  configured, tell the user to create `~/.config/session-vault/config` with
  those two variables (see the plugin README). Do not print the token back.
- If it warns about an unencrypted upload, relay the warning and mention that
  running `"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" keygen` once
  enables end-to-end encryption.
- Report the session id that was saved and any error verbatim.
