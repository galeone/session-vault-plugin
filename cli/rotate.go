package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// rotatingMarker is the first line of a key file while rotate-key runs.
// The identity right after it is the new key; the ones after that are the
// old keys, kept so an interrupted rotation can be resumed by running
// rotate-key again.
const rotatingMarker = "# session-vault rotate-key: in progress (new key first, then the old ones)"

// rotateKey replaces the age key: it re-encrypts every session on the
// server that the old key can read to a new key only, then leaves only
// the new key in the key file. The old key file is kept as a backup next
// to it. The steps are resumable: re-running after a failure continues
// with the same new key.
func rotateKey() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	newID, oldIDs, backup, err := prepareRotation(cfg.AgeKey)
	if err != nil {
		return err
	}

	metas, err := listSessions(cfg)
	if err != nil {
		return err
	}
	var rotated, current int
	var failed []string
	seen := map[string]bool{}
	for _, m := range metas {
		// The list is newest first, and the server serves an id's newest
		// copy: only that one is rotated (the upload supersedes the
		// others, which only exist for data stored by old servers).
		if seen[m.SessionID] {
			continue
		}
		seen[m.SessionID] = true
		done, err := rotateSession(cfg, m, newID, oldIDs)
		switch {
		case err != nil:
			fmt.Printf("failed  %s: %v\n", m.SessionID, err)
			failed = append(failed, m.SessionID)
		case done:
			fmt.Printf("rotated %s\n", m.SessionID)
			rotated++
		default:
			current++
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d session(s) not rotated (%s); the key file still holds the old key: fix the cause and run rotate-key again",
			len(failed), strings.Join(failed, ", "))
	}

	if err := writeKeyFile(cfg.AgeKey, "", newID); err != nil {
		return err
	}
	fmt.Printf("re-encrypted %d session(s) to the new key (%d already used it)\n", rotated, current)
	fmt.Printf("new key: %s (public key %s)\n", cfg.AgeKey, newID.Recipient())
	if backup != "" {
		fmt.Printf("old key kept at %s\n", backup)
	}
	fmt.Println("copy the new key to every machine that fetches these sessions: the old key can no longer decrypt them.")
	fmt.Println("once every machine has it, delete the old key backup.")
	return nil
}

// prepareRotation returns the new identity and the old ones. On a fresh
// rotation it backs up the key file, generates the new identity and writes
// the in-progress key file (new key first, old keys after, so saves made in
// the meantime are still readable by both). On a resumed rotation it
// reads them back from that file.
func prepareRotation(keyPath string) (*age.X25519Identity, []age.Identity, string, error) {
	data, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		return nil, nil, "", fmt.Errorf("no age key at %s: there is nothing to rotate", keyPath)
	}
	if err != nil {
		return nil, nil, "", err
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, nil, "", fmt.Errorf("parsing %s: %w", keyPath, err)
	}

	if strings.HasPrefix(string(data), rotatingMarker) {
		if len(ids) < 2 {
			return nil, nil, "", fmt.Errorf("%s is marked as mid-rotation but holds %d key(s); restore it from the backup next to it", keyPath, len(ids))
		}
		newID, ok := ids[0].(*age.X25519Identity)
		if !ok {
			return nil, nil, "", fmt.Errorf("unexpected identity type in %s", keyPath)
		}
		fmt.Println("resuming an interrupted rotation with the new key already in", keyPath)
		return newID, ids[1:], "", nil
	}

	backup := keyPath + "." + time.Now().UTC().Format("20060102T150405Z") + ".old"
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return nil, nil, "", fmt.Errorf("backing up the old key: %w", err)
	}
	newID, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, nil, "", err
	}
	old := make([]*age.X25519Identity, 0, len(ids))
	for _, id := range ids {
		x, ok := id.(*age.X25519Identity)
		if !ok {
			return nil, nil, "", fmt.Errorf("unexpected identity type in %s", keyPath)
		}
		old = append(old, x)
	}
	if err := writeKeyFile(keyPath, rotatingMarker, newID, old...); err != nil {
		return nil, nil, "", err
	}
	slog.Debug("rotate-key", "backup", backup, "new_recipient", newID.Recipient().String())
	return newID, ids, backup, nil
}

// writeKeyFile atomically replaces keyPath with the given identities
// (0600), the first one being the current key.
func writeKeyFile(keyPath, header string, first *age.X25519Identity, rest ...*age.X25519Identity) error {
	var b strings.Builder
	if header != "" {
		b.WriteString(header + "\n")
	}
	fmt.Fprintf(&b, "# created: %s\n# public key: %s\n%s\n", time.Now().Format(time.RFC3339), first.Recipient(), first)
	for _, id := range rest {
		fmt.Fprintf(&b, "# old key, removed when rotate-key finishes\n%s\n", id)
	}
	tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".age.key.*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), keyPath)
}

// rotateSession re-encrypts one stored session to newID when an old key
// can read it, and reports whether it uploaded a new copy. A session only
// the new key can read is already rotated and left alone; one that no key
// here can read is an error.
func rotateSession(cfg config, m Meta, newID *age.X25519Identity, oldIDs []age.Identity) (bool, error) {
	if !validPathName.MatchString(m.SessionID) || !validPathName.MatchString(m.Project) {
		return false, fmt.Errorf("server returned an invalid session id or project")
	}
	if m.Encrypted != "age+gzip" {
		return false, fmt.Errorf("unexpected encryption %q", m.Encrypted)
	}
	blob, err := fetchBlob(cfg, m.SessionID)
	if err != nil {
		return false, err
	}
	plain, err := age.Decrypt(bytes.NewReader(blob), oldIDs...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if !errors.As(err, &noMatch) {
			return false, fmt.Errorf("decrypting: %w", err)
		}
		if _, err := age.Decrypt(bytes.NewReader(blob), newID); err == nil {
			return false, nil // already encrypted to the new key only
		}
		return false, fmt.Errorf("neither the old nor the new key can decrypt it")
	}
	// The payload is gzip(transcript): re-encrypt it as is, without
	// decompressing, to the new key only.
	var out bytes.Buffer
	enc, err := age.Encrypt(&out, newID.Recipient())
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(enc, plain); err != nil {
		return false, fmt.Errorf("decrypting: %w", err)
	}
	if err := enc.Close(); err != nil {
		return false, err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.URL+"/api/v1/sessions", &out)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("X-Session-Id", m.SessionID)
	req.Header.Set("X-Project", m.Project)
	if m.Cwd != "" {
		req.Header.Set("X-Cwd", m.Cwd)
	}
	if m.Name != "" {
		req.Header.Set("X-Name", m.Name)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Encrypted", "age+gzip")
	var stored Meta
	if err := doJSON(req, &stored); err != nil {
		return false, fmt.Errorf("uploading: %w", err)
	}
	return true, nil
}

// fetchBlob downloads one stored blob, exactly as the server holds it.
func fetchBlob(cfg config, id string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.URL+"/api/v1/sessions/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	return io.ReadAll(resp.Body)
}
