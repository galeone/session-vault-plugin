---
description: Replace the session-vault encryption key and re-encrypt every saved session with the new one, with confirmation
allowed-tools: [Bash]
---

Rotate the user's session-vault age key: every session on the server that
the current key can read is re-encrypted to a new key, and the key file is
replaced with the new key (the old one is kept as a backup next to it).
Use it when the key may have leaked, or to retire a key copied to a
machine the user no longer controls.

1. Explain what will happen and ask the user to confirm. Make sure they
   understand that afterwards **every other machine needs the new key**
   (`~/.config/session-vault/age.key`): the old key will no longer decrypt
   the sessions. If they do not confirm, stop.
2. Once confirmed, run:

   ```
   "${CLAUDE_PLUGIN_ROOT}/scripts/session-vault.sh" rotate-key --yes
   ```

   and report the output verbatim, including where the old key was kept.
3. Remind the user to copy the new key to their other machines, keep a
   backup of it somewhere safe (a password manager works), and delete the
   old key backup once every machine has the new key.

If it fails partway (network error, a session no key can decrypt), relay
the error verbatim. Nothing is lost: the key file keeps both keys until
the rotation finishes, and running `/session-vault:rotate-key` again
resumes with the same new key.
