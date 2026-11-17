---
description: List the Claude Code sessions saved on the session-vault server
allowed-tools: [Bash]
---

List the user's saved sessions, using Bash:

```
"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" list
```

Show the table (NAME, SESSION ID, ORIGINAL CWD) and remind the user they
can restore one into the current project with
`/session-vault:fetch <name-or-id>`, or delete it with
`/session-vault:clean <name-or-id>`. If configuration is missing, point
the user to `~/.config/session-vault/config` (needs `SESSION_VAULT_URL`
and `SESSION_VAULT_TOKEN`).
