package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// newAgeKey writes a fresh age X25519 identity to a temp file and returns
// its path plus the identity (for decrypting in tests).
func newAgeKey(t *testing.T, extraIdentities ...*age.X25519Identity) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintln(&b, id.String())
	for _, x := range extraIdentities {
		fmt.Fprintln(&b, x.String())
	}
	path := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/home/u/my-proj":        "-home-u-my-proj",
		"/data/pgaleone/ue/bim2": "-data-pgaleone-ue-bim2",
		"a/b c/d":                "a-b-c-d",
	}
	for in, want := range cases {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("SESSION_VAULT_URL", "https://example.com")
	t.Setenv("SESSION_VAULT_TOKEN", "tok")
	t.Setenv("SESSION_VAULT_CONFIG", filepath.Join(t.TempDir(), "nope"))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://example.com" || cfg.Token != "tok" {
		t.Fatalf("bad cfg: %+v", cfg)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	content := "# comment\nexport SESSION_VAULT_URL='https://example.com'\nSESSION_VAULT_TOKEN = tok2\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_VAULT_CONFIG", cfgPath)
	t.Setenv("SESSION_VAULT_URL", "")
	t.Setenv("SESSION_VAULT_TOKEN", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://example.com" || cfg.Token != "tok2" {
		t.Fatalf("bad cfg: %+v", cfg)
	}
}

func TestLoadConfigEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte("SESSION_VAULT_URL=https://file.com\nSESSION_VAULT_TOKEN=filetok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_VAULT_CONFIG", cfgPath)
	t.Setenv("SESSION_VAULT_URL", "https://env.com")
	t.Setenv("SESSION_VAULT_TOKEN", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://env.com" {
		t.Fatalf("env URL should win: got %q", cfg.URL)
	}
	if cfg.Token != "filetok" {
		t.Fatalf("file token should fill empty env: got %q", cfg.Token)
	}
}

func TestLoadConfigMissingURL(t *testing.T) {
	t.Setenv("SESSION_VAULT_URL", "")
	t.Setenv("SESSION_VAULT_TOKEN", "tok")
	t.Setenv("SESSION_VAULT_CONFIG", filepath.Join(t.TempDir(), "nope"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestLoadConfigSchemelessURL(t *testing.T) {
	t.Setenv("SESSION_VAULT_URL", "example.com")
	t.Setenv("SESSION_VAULT_TOKEN", "tok")
	t.Setenv("SESSION_VAULT_CONFIG", filepath.Join(t.TempDir(), "nope"))
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestTrackFrom(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	payload := `{"session_id":"abc","transcript_path":"/tmp/t.jsonl","cwd":"/proj/x"}`
	if err := trackFrom(strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	slug := projectSlug("/proj/x")
	got, err := os.ReadFile(filepath.Join(state, "session-vault", "by-cwd", slug+".json"))
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if !strings.Contains(string(got), "abc") {
		t.Fatalf("state file missing session id: %s", got)
	}
}

func TestTrackFromBadInput(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// malformed JSON: must not error (best-effort hook)
	if err := trackFrom(strings.NewReader("{not json")); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	// empty cwd: no-op
	if err := trackFrom(strings.NewReader(`{"session_id":"x"}`)); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCurrentSessionFromState(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// fake transcript
	transcript := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"session_id":"abc","transcript_path":"%s","cwd":"/proj"}`, transcript)
	if err := trackFrom(strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	id, got, err := currentSession("/proj")
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc" || got != transcript {
		t.Fatalf("got %q %q", id, got)
	}
}

func TestIdentitiesNoKey(t *testing.T) {
	cfg := config{AgeKey: filepath.Join(t.TempDir(), "missing")}
	ids, err := cfg.identities()
	if err != nil || ids != nil {
		t.Fatalf("want nil,nil got %v,%v", ids, err)
	}
}

func TestRecipientsMultiple(t *testing.T) {
	// two identities in one key file
	id2, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := newAgeKey(t, id2)
	cfg := config{AgeKey: path}
	recs, err := cfg.recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 recipients, got %d", len(recs))
	}
	ids, err := cfg.identities()
	if err != nil || len(ids) != 2 {
		t.Fatalf("want 2 identities, got %d,%v", len(ids), err)
	}
}

func TestEncodeTranscriptGzipOnly(t *testing.T) {
	src := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(src, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := encodeTranscript(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	// should be a gzip stream
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("expected gzip, got %v", err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("roundtrip mismatch: %q", out)
	}
}

func TestEncodeTranscriptAge(t *testing.T) {
	path := newAgeKey(t)
	cfg := config{AgeKey: path}
	recs, err := cfg.recipients()
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(src, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := encodeTranscript(src, recs)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(enc, []byte("age-encryption.org/v1")) {
		t.Fatalf("expected age header, got %q", enc[:min(40, len(enc))])
	}
	// decrypt and gunzip
	ids, err := cfg.identities()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := age.Decrypt(bytes.NewReader(enc), ids...)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"secret":true}` {
		t.Fatalf("roundtrip mismatch: %q", out)
	}
}

// fakeVault is an httptest server that mimics the server API for client tests.
func fakeVault(t *testing.T) *httptest.Server {
	t.Helper()
	store := map[string][]byte{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		store[r.Header.Get("X-Session-Id")] = body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Meta{
			SessionID:  r.Header.Get("X-Session-Id"),
			Name:       r.Header.Get("X-Name"),
			Project:    r.Header.Get("X-Project"),
			Size:       int64(len(body)),
			UploadedAt: time.Now().UTC(),
			Encrypted:  r.Header.Get("X-Encrypted"),
		})
	})
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var out []Meta
		for id, b := range store {
			out = append(out, Meta{SessionID: id, Project: "proj", Size: int64(len(b)), UploadedAt: time.Now().UTC()})
		}
		if out == nil {
			out = []Meta{}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b, ok := store[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	})
	return httptest.NewServer(mux)
}

func TestResolveByID(t *testing.T) {
	ts := fakeVault(t)
	defer ts.Close()
	cfg := config{URL: ts.URL, Token: "tok"}
	// seed a session
	seed, _ := http.NewRequest("POST", ts.URL+"/api/v1/sessions", nil)
	seed.Header.Set("X-Session-Id", "abc")
	seed.Header.Set("X-Project", "proj")
	resp, err := http.DefaultClient.Do(seed)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	m, err := resolve(cfg, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "abc" {
		t.Fatalf("got %q", m.SessionID)
	}
}

func TestResolveByName(t *testing.T) {
	// hand-craft a server that returns named sessions
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Meta{
			{SessionID: "abc", Name: "my-name", Project: "p"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := config{URL: ts.URL, Token: "tok"}

	m, err := resolve(cfg, "my-name")
	if err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "abc" {
		t.Fatalf("got %q", m.SessionID)
	}
	// ambiguous: two with same name
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Meta{
			{SessionID: "a1", Name: "dup", Project: "p"},
			{SessionID: "a2", Name: "dup", Project: "p"},
		})
	})
	ts2 := httptest.NewServer(mux2)
	defer ts2.Close()
	cfg2 := config{URL: ts2.URL, Token: "tok"}
	if _, err := resolve(cfg2, "dup"); err == nil || !strings.Contains(err.Error(), "matches") {
		t.Fatalf("expected ambiguous error, got %v", err)
	}
}

func TestDownloadSkipPresent(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", projects)
	cfg := config{URL: "http://unused", Token: "tok"}
	meta := Meta{SessionID: "abc", Project: "proj"}
	// pre-create the destination
	dest := filepath.Join(projects, "projects", "proj", "abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := download(cfg, meta, "proj"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "existing" {
		t.Fatalf("should have skipped, got %q", got)
	}
}

func TestSessionTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	content := "{}\n" +
		`{"type":"user","message":{"content":"hi"}}` + "\n" +
		`{"type":"ai-title","aiTitle":"first title","sessionId":"x"}` + "\n" +
		`{"type":"ai-title","aiTitle":"Second, Better Title!","sessionId":"x"}` + "\n" +
		`{"type":"cost-state","sessionId":"x"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if got := sessionTitle(path); got != "Second, Better Title!" {
		t.Fatalf("sessionTitle = %q, want the last ai-title", got)
	}

	noTitle := filepath.Join(dir, "notitle.jsonl")
	if err := os.WriteFile(noTitle, []byte(`{"type":"user"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := sessionTitle(noTitle); got != "" {
		t.Fatalf("sessionTitle = %q, want empty", got)
	}
	if got := sessionTitle(filepath.Join(dir, "missing.jsonl")); got != "" {
		t.Fatalf("sessionTitle on missing file = %q, want empty", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Second, Better Title!":           "second-better-title",
		"  Apple  access  ":               "apple-access",
		"keep_underscores-yes":            "keep_underscores-yes",
		"café au lait":                    "caf-au-lait",
		"":                                "",
		"-leading-dash-":                  "leading-dash",
		"only" + strings.Repeat("-x", 40): "only" + strings.Repeat("-x", 30), // trimmed to 64
	}
	for in, want := range cases {
		got := slugify(in)
		if got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !validSessionName.MatchString(got) {
			t.Errorf("slugify(%q) = %q is not a valid session name", in, got)
		}
	}
}

func TestGenerateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "age.key")

	rec, err := generateKey(path)
	if err != nil {
		t.Fatalf("generateKey: %v", err)
	}
	if rec == nil {
		t.Fatal("want a non-nil recipient")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want mode 0600, got %v", info.Mode().Perm())
	}
	// The written file must parse back to the same recipient.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := age.ParseIdentities(f)
	if err := f.Close(); err != nil {
		t.Fatalf("closing key file: %v", err)
	}
	if err != nil {
		t.Fatalf("key file does not parse: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 identity, got %d", len(ids))
	}
	// Overwriting must be refused.
	if _, err := generateKey(path); err == nil {
		t.Fatal("want error when overwriting an existing key")
	}
}
