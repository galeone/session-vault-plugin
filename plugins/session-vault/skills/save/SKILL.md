---
description: Save the current Claude Code session transcript to the configured session-vault server, optionally under a memorable name
allowed-tools: [Bash]
argument-hint: "[name]"
---

Upload the current session's transcript to the user's session-vault server.

Run this command with Bash and show the user its output. If the user provided
an argument, pass it as the session's name (letters, digits, `-`, `_` only —
slugify it if needed) so they can later restore with `/session-vault:fetch <name>`.
If the user gave no argument, the CLI automatically names the session after
the title Claude Code generated for it (the one shown in the terminal title);
mention that name in your summary:

```
"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" save --session "${CLAUDE_SESSION_ID}" [name]
```

Keep the `--session "${CLAUDE_SESSION_ID}"` argument exactly as written: it
identifies this session even if the shell has changed directory.

- If it fails because `SESSION_VAULT_URL` or `SESSION_VAULT_TOKEN` is not
  configured, tell the user to create `~/.config/session-vault/config` with
  those two variables (see the plugin README). Do not print the token back.
- If it reports that it generated a new age key, relay that notice verbatim:
  the key makes the upload end-to-end encrypted, and the user must copy the
  key file to every other machine that will fetch these sessions.
- Report the session id that was saved and any error verbatim.
