---
description: Show, turn on, or turn off automatic saving of Claude Code sessions to session-vault when they end
allowed-tools: [Bash]
argument-hint: "[on | off]"
---

Sessions are saved to the user's session-vault server automatically when
they end (on by default). Use `"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh"`
with Bash:

- **No arguments**: run `... autosave` and tell the user whether autosave
  is on or off and where that setting comes from.
- **`on` or `off`**: run `... autosave on` or `... autosave off`, which
  writes `SESSION_VAULT_AUTOSAVE` to `~/.config/session-vault/config`.
  Relay its output, including any note that an environment variable
  overrides the config file.

Mention that autosave never asks anything and never prints: it runs in the
background after the session ends and logs to
`~/.local/state/session-vault/logs/`. It skips sessions without a reply
from Claude, sessions unchanged since their last save, and sessions the
server holds a newer save of (for example one continued on another
machine). It also never creates an encryption key: until the user has run
`/session-vault:save` once on this machine, or copied `age.key` here,
nothing is saved automatically.
