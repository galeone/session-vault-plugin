package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
)

// memVault is an in-memory session-vault server: list, download, upload.
type memVault struct {
	mu       sync.Mutex
	metas    map[string]Meta
	blobs    map[string][]byte
	failPost map[string]int // session id -> uploads still to fail
	// plainOnly makes it behave like a server older than the bundle
	// scheme, refusing anything but age+gzip.
	plainOnly bool
	// limit, when set, refuses new session ids past it with 402, like a
	// hosted plan.
	limit int
}

func newMemVault(t *testing.T) (*memVault, *httptest.Server) {
	t.Helper()
	v := &memVault{metas: map[string]Meta{}, blobs: map[string][]byte{}, failPost: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		out := []Meta{}
		for _, m := range v.metas {
			out = append(out, m)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		b, ok := v.blobs[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		id := r.Header.Get("X-Session-Id")
		if v.failPost[id] > 0 {
			v.failPost[id]--
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if enc := r.Header.Get("X-Encrypted"); enc != schemeTranscript && (v.plainOnly || enc != schemeBundle) {
			http.Error(w, "uploads must be end-to-end encrypted (X-Encrypted: age+gzip)", http.StatusBadRequest)
			return
		}
		if _, exists := v.metas[id]; v.limit > 0 && !exists && len(v.metas) >= v.limit {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"session limit reached","plan":"free","limit":1,"used":1,"upgrade_url":"https://vault.example/account"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		m := Meta{
			SessionID: id, Project: r.Header.Get("X-Project"), Cwd: r.Header.Get("X-Cwd"),
			Name: r.Header.Get("X-Name"), Size: int64(len(body)), UploadedAt: time.Now(),
			Encrypted: r.Header.Get("X-Encrypted"),
		}
		v.metas[id], v.blobs[id] = m, body
		_ = json.NewEncoder(w).Encode(m)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return v, ts
}

// put stores transcript encrypted to recs, as save would.
func (v *memVault) put(t *testing.T, id, name, transcript string, recs ...age.Recipient) {
	t.Helper()
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(transcript))
	_ = w.Close()
	var out bytes.Buffer
	enc, err := age.Encrypt(&out, recs...)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = enc.Write(gz.Bytes())
	_ = enc.Close()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.metas[id] = Meta{SessionID: id, Project: "-work-repo", Cwd: "/work/repo", Name: name,
		Size: int64(out.Len()), UploadedAt: time.Now(), Encrypted: "age+gzip"}
	v.blobs[id] = out.Bytes()
}

// open decrypts a stored blob with ids and returns the transcript.
func (v *memVault) open(id string, ids ...age.Identity) (string, error) {
	v.mu.Lock()
	blob := v.blobs[id]
	v.mu.Unlock()
	r, err := age.Decrypt(bytes.NewReader(blob), ids...)
	if err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(gz)
	return string(b), err
}

func setupRotation(t *testing.T) (*memVault, string, *age.X25519Identity) {
	t.Helper()
	v, ts := newMemVault(t)
	keyPath := newAgeKey(t)
	ids, err := config{AgeKey: keyPath}.identities()
	if err != nil {
		t.Fatal(err)
	}
	old := ids[0].(*age.X25519Identity)
	t.Setenv("SESSION_VAULT_URL", ts.URL)
	t.Setenv("SESSION_VAULT_TOKEN", "tok")
	t.Setenv("SESSION_VAULT_AGE_KEY", keyPath)
	t.Setenv("SESSION_VAULT_CONFIG", filepath.Join(t.TempDir(), "none"))
	return v, keyPath, old
}

func keyFileIdentities(t *testing.T, path string) []age.Identity {
	t.Helper()
	ids, err := config{AgeKey: path}.identities()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestRotateKey(t *testing.T) {
	v, keyPath, old := setupRotation(t)
	v.put(t, "s1", "first", `{"a":1}`, old.Recipient())
	v.put(t, "s2", "", `{"b":2}`, old.Recipient())

	if err := rotateKey(); err != nil {
		t.Fatal(err)
	}

	ids := keyFileIdentities(t, keyPath)
	if len(ids) != 1 {
		t.Fatalf("key file holds %d identities, want only the new one", len(ids))
	}
	newID := ids[0].(*age.X25519Identity)
	if newID.String() == old.String() {
		t.Fatal("key was not replaced")
	}
	for id, want := range map[string]string{"s1": `{"a":1}`, "s2": `{"b":2}`} {
		got, err := v.open(id, newID)
		if err != nil || got != want {
			t.Errorf("%s with the new key = %q, %v; want %q", id, got, err, want)
		}
		if _, err := v.open(id, old); err == nil {
			t.Errorf("%s is still readable with the old key", id)
		}
	}
	if m := v.metas["s1"]; m.Name != "first" || m.Cwd != "/work/repo" || m.Project != "-work-repo" {
		t.Errorf("metadata not preserved: %+v", m)
	}
	backups, _ := filepath.Glob(keyPath + ".*.old")
	if len(backups) != 1 {
		t.Fatalf("want one old-key backup, got %v", backups)
	}
	if b := keyFileIdentities(t, backups[0]); b[0].(*age.X25519Identity).String() != old.String() {
		t.Error("the backup does not hold the old key")
	}
}

func TestRotateKeyResumesAfterFailure(t *testing.T) {
	v, keyPath, old := setupRotation(t)
	v.put(t, "s1", "", `{"a":1}`, old.Recipient())
	v.put(t, "s2", "", `{"b":2}`, old.Recipient())
	v.failPost["s2"] = 1

	if err := rotateKey(); err == nil {
		t.Fatal("rotation with a failed upload succeeded")
	}
	data, _ := os.ReadFile(keyPath)
	if !strings.HasPrefix(string(data), rotatingMarker) {
		t.Fatal("key file not marked as mid-rotation")
	}
	mid := keyFileIdentities(t, keyPath)
	if len(mid) != 2 || mid[1].(*age.X25519Identity).String() != old.String() {
		t.Fatal("mid-rotation key file must hold the new key, then the old one")
	}
	newID := mid[0].(*age.X25519Identity)

	if err := rotateKey(); err != nil {
		t.Fatal(err)
	}
	ids := keyFileIdentities(t, keyPath)
	if len(ids) != 1 || ids[0].(*age.X25519Identity).String() != newID.String() {
		t.Fatal("the resumed rotation did not keep the same new key")
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := v.open(id, newID); err != nil {
			t.Errorf("%s not readable with the new key: %v", id, err)
		}
		if _, err := v.open(id, old); err == nil {
			t.Errorf("%s still readable with the old key", id)
		}
	}
	if backups, _ := filepath.Glob(keyPath + ".*.old"); len(backups) != 1 {
		t.Fatalf("a resumed rotation must not back up again, got %v", backups)
	}
}

func TestRotateKeyRefusesForeignSessions(t *testing.T) {
	v, keyPath, _ := setupRotation(t)
	other, _ := age.GenerateX25519Identity()
	v.put(t, "s1", "", `{"a":1}`, other.Recipient())

	if err := rotateKey(); err == nil {
		t.Fatal("rotation succeeded although a session is unreadable")
	}
	data, _ := os.ReadFile(keyPath)
	if !strings.HasPrefix(string(data), rotatingMarker) {
		t.Fatal("the old key must stay in the key file when a session could not be rotated")
	}
}

func TestRotateKeyWithoutKey(t *testing.T) {
	setupRotation(t)
	t.Setenv("SESSION_VAULT_AGE_KEY", filepath.Join(t.TempDir(), "missing.key"))
	if err := rotateKey(); err == nil {
		t.Fatal("rotation without a key succeeded")
	}
}

func TestDownloadSameContentKeepsLocalCopy(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", projects)
	v, ts := newMemVault(t)
	keyPath := newAgeKey(t)
	cfg := config{URL: ts.URL, Token: "tok", AgeKey: keyPath}
	recs, err := cfg.recipients()
	if err != nil {
		t.Fatal(err)
	}
	v.put(t, "abc", "", `{"a":1}`, recs...)

	dest := filepath.Join(projects, "projects", "proj", "abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dest, past, past); err != nil {
		t.Fatal(err)
	}

	written, err := download(cfg, v.metas["abc"], "proj")
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("identical content reported as written")
	}
	if baks, _ := filepath.Glob(dest + ".*.bak"); len(baks) != 0 {
		t.Errorf("identical content left backups: %v", baks)
	}
	if info, _ := os.Stat(dest); !info.ModTime().After(past) {
		t.Error("local copy not marked current")
	}
}
