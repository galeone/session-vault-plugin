---
description: Delete a saved session from the session-vault server, with confirmation
allowed-tools: [Bash]
argument-hint: "<session-id-or-name>"
---

Delete a session from the user's session-vault server. This is permanent:
the server-side copy is gone and cannot be recovered.

1. If the user gave no argument, run
   `"${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" list` and ask which
   session they want to delete.
2. Show the session that will be deleted (name, id, original cwd) and ask
   the user to confirm. If they do not confirm, stop — do not delete.
3. Once confirmed, run:

   ```
   "${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" clean <name-or-id>
   ```

   and report the output verbatim.

Deleting only removes the copy on the server; local transcripts on this
machine are not touched. If an ambiguous name matches several sessions,
relay the error (it lists the ids) and ask the user to pick one by id.
