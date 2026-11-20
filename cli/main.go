// session-vault is the client CLI behind the session-vault Claude Code
// plugin. It uploads Claude Code session transcripts to a session-vault
// server (/session-vault:save) and downloads them back (/session-vault:fetch), end-to-end encrypted with
// age when a key is configured.
//
// It is a single static binary with no runtime dependencies, so the plugin
// works the same on Linux, macOS, and native Windows (Git Bash).
//
// Usage:
//
//	session-vault keygen            generate the age identity (once per user)
//	session-vault track             SessionStart hook: record session info from stdin JSON
//	session-vault save [--session <id>] [name]
//	                                upload the current session (auto-named from the session title)
//	session-vault list              list sessions stored on the server
//	session-vault get <session-id>  download one session into ~/.claude/projects/
//	session-vault all               download every session missing locally
//	session-vault clean <id-or-name> delete one session from the server
//	session-vault clean-all --yes   delete every session from the server
//	session-vault version           print the version
//
// Configuration comes from ~/.config/session-vault/config (KEY=VALUE lines)
// or environment variables (which win): SESSION_VAULT_URL,
// SESSION_VAULT_TOKEN, SESSION_VAULT_AGE_KEY (default: age.key next to the
// config file).
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"filippo.io/age"
)

var version = "dev"

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// httpClient bounds every phase of a request that can hang on a dead or
// slow server — connect, TLS, waiting for the response headers — without
// capping the total time, which must stay open-ended for large uploads
// and downloads on slow links.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

type Meta struct {
	SessionID  string    `json:"session_id"`
	Name       string    `json:"name,omitempty"`
	Project    string    `json:"project"`
	Cwd        string    `json:"cwd,omitempty"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	Encrypted  string    `json:"encrypted,omitempty"`
}

func main() {
	initLogger()
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen()
	case "track":
		err = track()
	case "save":
		var name, sessionID string
		args := os.Args[2:]
		for len(args) > 0 {
			switch {
			case args[0] == "--session" && len(args) > 1:
				sessionID, args = args[1], args[2:]
			case strings.HasPrefix(args[0], "--session="):
				sessionID, args = strings.TrimPrefix(args[0], "--session="), args[1:]
			case name == "" && !strings.HasPrefix(args[0], "--"):
				name, args = args[0], args[1:]
			default:
				usage()
			}
		}
		err = save(name, sessionID)
	case "list":
		err = list()
	case "get":
		if len(os.Args) != 3 {
			usage()
		}
		err = get(os.Args[2])
	case "all":
		err = fetchAll()
	case "clean":
		if len(os.Args) != 3 {
			usage()
		}
		err = clean(os.Args[2])
	case "clean-all":
		err = cleanAll(len(os.Args) > 2 && os.Args[2] == "--yes")
	case "version":
		fmt.Println("session-vault", version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "session-vault:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: session-vault <command>

  keygen            generate the age identity used for end-to-end encryption
  track             (hook) record the current session from stdin JSON
  save [--session <id>] [name]
                    upload the current session, optionally labeled; --session
                    names it exactly (the skill passes Claude Code's id),
                    otherwise it is resolved from the working directory
  list              list sessions stored on the server
  get <id-or-name>  restore one session into the CURRENT project directory,
                    so it is resumable from here with claude --resume
  all               mirror every missing session to its original project dir
  clean <id-or-name> delete one session from the server (local files untouched)
  clean-all --yes   delete every session from the server (refused without --yes)
  version           print the version`)
	os.Exit(2)
}

// ---------- paths & config ----------

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "session-vault")
	}
	return filepath.Join(homeDir(), ".config", "session-vault")
}

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "session-vault")
	}
	return filepath.Join(homeDir(), ".local", "state", "session-vault")
}

// claudeProjectsDir is where Claude Code stores transcripts:
// <config dir>/projects/<cwd-slug>/<session-id>.jsonl
func claudeProjectsDir() string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(homeDir(), ".claude")
	}
	return filepath.Join(base, "projects")
}

// projectSlug mirrors Claude Code's project directory naming: every
// non-alphanumeric character of the absolute cwd becomes '-'.
func projectSlug(cwd string) string {
	return nonAlnum.ReplaceAllString(cwd, "-")
}

type config struct {
	URL    string
	Token  string
	AgeKey string
}

func loadConfig() (config, error) {
	cfg := config{
		URL:    os.Getenv("SESSION_VAULT_URL"),
		Token:  os.Getenv("SESSION_VAULT_TOKEN"),
		AgeKey: os.Getenv("SESSION_VAULT_AGE_KEY"),
	}
	path := os.Getenv("SESSION_VAULT_CONFIG")
	if path == "" {
		path = filepath.Join(configDir(), "config")
	}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.TrimSpace(key) {
			case "SESSION_VAULT_URL":
				if cfg.URL == "" {
					cfg.URL = value
				}
			case "SESSION_VAULT_TOKEN":
				if cfg.Token == "" {
					cfg.Token = value
				}
			case "SESSION_VAULT_AGE_KEY":
				if cfg.AgeKey == "" {
					cfg.AgeKey = value
				}
			}
		}
	}
	if cfg.AgeKey == "" {
		cfg.AgeKey = filepath.Join(configDir(), "age.key")
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("SESSION_VAULT_URL not set (export it or add it to %s)", path)
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("SESSION_VAULT_TOKEN not set (export it or add it to %s)", path)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	if !strings.Contains(cfg.URL, "://") {
		return cfg, fmt.Errorf("SESSION_VAULT_URL must include a scheme (want e.g. https://sessions.example.com), got %q", cfg.URL)
	}
	return cfg, nil
}

// identities returns all age identities in the key file, or nil when
// encryption is not configured (no key file).
func (c config) identities() ([]age.Identity, error) {
	f, err := os.Open(c.AgeKey)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", c.AgeKey, err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no age identity found in %s", c.AgeKey)
	}
	return ids, nil
}

// recipients returns the public recipient of every X25519 identity in the
// key file, or nil when encryption is not configured. Sessions are encrypted
// to all of them, so a key file can hold several identities.
func (c config) recipients() ([]age.Recipient, error) {
	ids, err := c.identities()
	if err != nil || ids == nil {
		return nil, err
	}
	recs := make([]age.Recipient, 0, len(ids))
	for _, id := range ids {
		// X25519Identity is the only identity type age.ParseIdentities returns.
		if x, ok := id.(*age.X25519Identity); ok {
			recs = append(recs, x.Recipient())
		}
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("no X25519 identity found in %s", c.AgeKey)
	}
	return recs, nil
}

// ---------- commands ----------

// generateKey writes a fresh age X25519 identity to keyPath (0600, parent
// dir 0700) and returns its public recipient. It refuses to replace an
// existing file: a lost key means unrecoverable sessions, so overwrite is a
// deliberate act (delete the file first).
func generateKey(keyPath string) (age.Recipient, error) {
	if _, err := os.Stat(keyPath); err == nil {
		return nil, fmt.Errorf("refusing to overwrite existing key at %s", keyPath)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, err
	}
	content := fmt.Sprintf("# created: %s\n# public key: %s\n%s\n",
		time.Now().Format(time.RFC3339), id.Recipient(), id)
	if err := os.WriteFile(keyPath, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return id.Recipient(), nil
}

func keygen() error {
	keyPath := os.Getenv("SESSION_VAULT_AGE_KEY")
	if keyPath == "" {
		keyPath = filepath.Join(configDir(), "age.key")
	}
	rec, err := generateKey(keyPath)
	if err != nil {
		return err
	}
	fmt.Println("wrote", keyPath)
	fmt.Println("public key:", rec)
	fmt.Println("copy this file to every machine that needs to fetch your sessions;")
	fmt.Println("if you lose it, encrypted sessions are unrecoverable")
	return nil
}

// track is the SessionStart hook: Claude Code pipes the hook payload as JSON
// on stdin. State is keyed by cwd slug so concurrent sessions in different
// projects don't clobber each other. Best-effort: never fails the hook.
func track() error {
	return trackFrom(os.Stdin)
}

// trackFrom is the testable core of track: it reads the hook payload from r
// and records it under the cwd slug.
func trackFrom(r io.Reader) error {
	var payload struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil || payload.Cwd == "" {
		return nil
	}
	dir := filepath.Join(stateDir(), "by-cwd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil
	}
	state := filepath.Join(dir, projectSlug(payload.Cwd)+".json")
	_ = os.WriteFile(state, data, 0o600)
	slog.Debug("track", "session", payload.SessionID, "cwd", payload.Cwd, "state", state)
	return nil
}

// sessionByID locates the transcript of a known session id, wherever its
// project lives: first via the state the track hook recorded (which also
// knows the session's cwd), then by searching every project directory.
// It is what makes save independent of the shell's current directory,
// which drifts when Claude cd's into a subdirectory.
func sessionByID(id string) (transcript, project, cwd string, err error) {
	states, _ := filepath.Glob(filepath.Join(stateDir(), "by-cwd", "*.json"))
	for _, path := range states {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st struct {
			SessionID      string `json:"session_id"`
			TranscriptPath string `json:"transcript_path"`
			Cwd            string `json:"cwd"`
		}
		if json.Unmarshal(data, &st) != nil || st.SessionID != id || st.TranscriptPath == "" || st.Cwd == "" {
			continue
		}
		if _, err := os.Stat(st.TranscriptPath); err == nil {
			return st.TranscriptPath, projectSlug(st.Cwd), st.Cwd, nil
		}
	}
	matches, _ := filepath.Glob(filepath.Join(claudeProjectsDir(), "*", id+".jsonl"))
	if len(matches) > 0 {
		// The project slug is the directory name; the original cwd is not
		// recoverable from it, so the caller keeps its own.
		return matches[0], filepath.Base(filepath.Dir(matches[0])), "", nil
	}
	return "", "", "", fmt.Errorf("no transcript found for session %s", id)
}

// currentSession resolves the session id and transcript path for cwd: first
// from the state file written by the track hook, then by falling back to the
// most recently modified transcript for the project.
func currentSession(cwd string) (id, transcript string, err error) {
	slug := projectSlug(cwd)

	if data, err := os.ReadFile(filepath.Join(stateDir(), "by-cwd", slug+".json")); err == nil {
		var st struct {
			SessionID      string `json:"session_id"`
			TranscriptPath string `json:"transcript_path"`
		}
		if json.Unmarshal(data, &st) == nil && st.TranscriptPath != "" {
			if _, statErr := os.Stat(st.TranscriptPath); statErr == nil {
				return st.SessionID, st.TranscriptPath, nil
			}
		}
	}

	matches, _ := filepath.Glob(filepath.Join(claudeProjectsDir(), slug, "*.jsonl"))
	var newest string
	var newestTime time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err == nil && info.ModTime().After(newestTime) {
			newest, newestTime = m, info.ModTime()
		}
	}
	if newest == "" {
		return "", "", fmt.Errorf("no session transcript found for %s", cwd)
	}
	return strings.TrimSuffix(filepath.Base(newest), ".jsonl"), newest, nil
}

var validSessionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validPathName is what a server-supplied session id or project slug must
// look like before it is used as a path component (same rule the server
// enforces on upload).
var validPathName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func save(name, sessionID string) error {
	if name != "" && !validSessionName.MatchString(name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '-' and '_' (max 64 chars)", name)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var id, transcript, project string
	// An empty or malformed --session (e.g. an older Claude Code that does
	// not substitute ${CLAUDE_SESSION_ID}) falls back to the cwd lookup.
	if validPathName.MatchString(sessionID) {
		var sessCwd string
		if transcript, project, sessCwd, err = sessionByID(sessionID); err == nil {
			id = sessionID
			if sessCwd != "" {
				cwd = sessCwd
			}
		} else {
			slog.Debug("session lookup by id failed, falling back to cwd", "session_id", sessionID, "error", err)
		}
	}
	if transcript == "" {
		if id, transcript, err = currentSession(cwd); err != nil {
			return err
		}
		project = projectSlug(cwd)
	}
	if name == "" {
		if title := sessionTitle(transcript); title != "" {
			name = slugify(title)
			fmt.Fprintf(os.Stderr, "no name given; using the session title %q\n", title)
		}
	}

	recipients, err := cfg.recipients()
	if err != nil {
		return err
	}
	if recipients == nil {
		// No key yet: generate one so every upload is end-to-end encrypted
		// from the first save, without the user running keygen by hand.
		rec, err := generateKey(cfg.AgeKey)
		if err != nil {
			return err
		}
		recipients = []age.Recipient{rec}
		fmt.Fprintf(os.Stderr, "generated a new age key at %s for end-to-end encryption.\n", cfg.AgeKey)
		fmt.Fprintf(os.Stderr, "Copy this file to every machine that will fetch these sessions;\n")
		fmt.Fprintf(os.Stderr, "if you lose it, the sessions are unrecoverable.\n")
	}

	body, err := encodeTranscript(transcript, recipients)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.URL+"/api/v1/sessions", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("X-Session-Id", id)
	req.Header.Set("X-Project", project)
	req.Header.Set("X-Cwd", cwd)
	if name != "" {
		req.Header.Set("X-Name", name)
	}
	// Every upload is age(gzip(transcript)): a key is generated above when
	// none exists, so there is no plaintext upload path.
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Encrypted", "age+gzip")

	var meta Meta
	if err := doJSON(req, &meta); err != nil {
		return err
	}
	enc := "encrypted"
	if meta.Encrypted == "" {
		// The server did not record the scheme we declared: still
		// ciphertext on disk, but say so rather than claim otherwise.
		enc = "encrypted, scheme not recorded by the server"
	}
	label := ""
	if meta.Name != "" {
		label = fmt.Sprintf(" as %q", meta.Name)
	}
	fmt.Printf("saved session %s%s (%s, %d bytes stored) from %s\n", meta.SessionID, label, enc, meta.Size, transcript)
	slog.Debug("save", "session_id", meta.SessionID, "name", meta.Name, "size", meta.Size, "encrypted", meta.Encrypted, "transcript", transcript)
	return nil
}

// encodeTranscript streams age(gzip(transcript)), encrypting to every
// configured recipient. There is no plaintext encoding: at least one
// recipient is required.
func encodeTranscript(path string, recipients []age.Recipient) (io.Reader, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no age recipient: refusing to upload unencrypted")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = f.Close() }()
		enc, err := age.Encrypt(pw, recipients...)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		gz := gzip.NewWriter(enc)
		if _, err := io.Copy(gz, f); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := gz.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := enc.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr, nil
}

func list() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	metas, err := listSessions(cfg)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tSESSION ID\tORIGINAL CWD\tUPLOADED AT\tBYTES\tENCRYPTED"); err != nil {
		return err
	}
	for _, m := range metas {
		enc := m.Encrypted
		if enc == "" {
			enc = "no"
		}
		name := m.Name
		if name == "" {
			name = "-"
		}
		cwd := m.Cwd
		if cwd == "" {
			cwd = m.Project
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			name, m.SessionID, cwd, m.UploadedAt.Format(time.RFC3339), m.Size, enc); err != nil {
			return err
		}
	}
	return w.Flush()
}

func listSessions(cfg config) ([]Meta, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.URL+"/api/v1/sessions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	var metas []Meta
	err = doJSON(req, &metas)
	if err == nil {
		slog.Debug("list", "count", len(metas))
	}
	return metas, err
}

// get restores one session — addressed by name or session id — into the
// CURRENT working directory's project, so it is immediately resumable from
// here with `claude --resume <id>` even when the original machine, OS, or
// path was different.
func get(key string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	meta, err := resolve(cfg, key)
	if err != nil {
		return err
	}
	slog.Debug("resolve", "key", key, "session_id", meta.SessionID, "project", meta.Project, "cwd", meta.Cwd)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := download(cfg, meta, projectSlug(cwd)); err != nil {
		return err
	}
	if meta.Cwd != "" && meta.Cwd != cwd {
		fmt.Printf("note: this session was recorded in %s; its history references paths from there\n", meta.Cwd)
	}
	fmt.Printf("resume it from this directory with: claude --resume %s\n", meta.SessionID)
	return nil
}

// sessionTitle returns the AI-generated title Claude Code writes into the
// transcript ("type":"ai-title" lines, repeated as the title is
// regenerated); the last occurrence wins. Empty when the transcript has
// none.
func sessionTitle(transcript string) string {
	f, err := os.Open(transcript)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var title string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			AITitle string `json:"aiTitle"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Type == "ai-title" && line.AITitle != "" {
			title = line.AITitle
		}
	}
	return title
}

// slugify turns a free-form title into a valid session name: lowercase,
// runs of non-alphanumerics collapse to a single '-', max 64 chars.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteRune('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > 64 {
		out = strings.Trim(string(r[:64]), "-")
	}
	return out
}

// resolve matches key against session ids first, then against user-chosen
// names. An ambiguous name (reused across saves) is an error listing the ids.
func resolve(cfg config, key string) (Meta, error) {
	metas, err := listSessions(cfg)
	if err != nil {
		return Meta{}, err
	}
	var byName []Meta
	for _, m := range metas {
		if m.SessionID == key {
			return m, nil
		}
		if m.Name == key {
			byName = append(byName, m)
		}
	}
	switch len(byName) {
	case 0:
		return Meta{}, fmt.Errorf("no session with id or name %q (see: session-vault list)", key)
	case 1:
		return byName[0], nil
	default:
		ids := make([]string, len(byName))
		for i, m := range byName {
			ids[i] = m.SessionID
		}
		return Meta{}, fmt.Errorf("name %q matches %d sessions (%s); use a session id", key, len(byName), strings.Join(ids, ", "))
	}
}

func fetchAll() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	metas, err := listSessions(cfg)
	if err != nil {
		return err
	}
	for _, m := range metas {
		// Mirror to the original project directory: `all` is a backup
		// restore, unlike `get` which pulls a session into the cwd.
		if err := download(cfg, m, m.Project); err != nil {
			return fmt.Errorf("session %s: %w", m.SessionID, err)
		}
	}
	return nil
}

func deleteOne(cfg config, m Meta) error {
	req, err := http.NewRequest(http.MethodDelete, cfg.URL+"/api/v1/sessions/"+m.SessionID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	var out struct {
		Deleted int `json:"deleted"`
	}
	if err := doJSON(req, &out); err != nil {
		return err
	}
	return nil
}

// clean deletes one session (by id or name) from the server. Local
// transcripts are never touched.
func clean(key string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	m, err := resolve(cfg, key)
	if err != nil {
		return err
	}
	if err := deleteOne(cfg, m); err != nil {
		return err
	}
	if m.Name != "" {
		fmt.Printf("deleted session %s (%s)\n", m.SessionID, m.Name)
	} else {
		fmt.Printf("deleted session %s\n", m.SessionID)
	}
	return nil
}

// cleanAll deletes every session the token can see. It is deliberately
// unusable without an explicit --yes, because it is permanent and
// irreversible.
func cleanAll(yes bool) error {
	if !yes {
		return fmt.Errorf("refusing to delete every session without --yes")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	metas, err := listSessions(cfg)
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Println("no sessions on the server")
		return nil
	}
	deleted, failed := 0, []string{}
	for _, m := range metas {
		if err := deleteOne(cfg, m); err != nil {
			failed = append(failed, m.SessionID)
			continue
		}
		deleted++
	}
	fmt.Printf("deleted %d session(s)\n", deleted)
	if len(failed) > 0 {
		return fmt.Errorf("could not delete %d: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// download fetches meta's blob and writes the decrypted transcript into the
// given local project slug.
func download(cfg config, meta Meta, destProject string) error {
	// Session ids and project slugs come from the server and become file
	// names under ~/.claude/projects: a compromised server must not be able
	// to steer writes elsewhere ("../..") or into odd paths.
	id := meta.SessionID
	if !validPathName.MatchString(id) {
		return fmt.Errorf("server returned an invalid session id %q", id)
	}
	if !validPathName.MatchString(destProject) {
		return fmt.Errorf("server returned an invalid project %q for session %s", destProject, id)
	}
	destDir := filepath.Join(claudeProjectsDir(), destProject)
	dest := filepath.Join(destDir, id+".jsonl")
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("skip    %s (already present at %s)\n", id, dest)
		slog.Debug("download", "session_id", id, "skip", true, "dest", dest)
		return nil
	}
	switch meta.Encrypted {
	case "age+gzip":
	case "":
		// Every client save is age-encrypted, so a session the server
		// reports as plaintext was not produced by us: accepting it would
		// let a malicious server plant a transcript Claude Code then
		// resumes (it cannot forge age ciphertext for our key, but it can
		// claim "unencrypted").
		return fmt.Errorf("session %s is not end-to-end encrypted; refusing to restore it", id)
	default:
		return fmt.Errorf("unknown encryption scheme %q; update the plugin", meta.Encrypted)
	}
	identities, err := cfg.identities()
	if err != nil {
		return err
	}
	if identities == nil {
		return fmt.Errorf("session is encrypted but no age key at %s (copy it from the machine that ran keygen)", cfg.AgeKey)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, cfg.URL+"/api/v1/sessions/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}

	dec, err := age.Decrypt(resp.Body, identities...)
	if err != nil {
		return fmt.Errorf("decrypting (wrong key?): %w", err)
	}
	src, err := gzip.NewReader(dec)
	if err != nil {
		return fmt.Errorf("decompressing: %w", err)
	}

	// Write to a temp file first so a failed download or decrypt never
	// leaves a corrupt transcript where Claude Code would load it.
	tmp, err := os.CreateTemp(destDir, "."+id+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	fmt.Printf("fetched %s -> %s\n", id, dest)
	slog.Debug("download", "session_id", id, "dest", dest, "encrypted", meta.Encrypted)
	return nil
}

// ---------- http helpers ----------

func doJSON(req *http.Request, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return httpError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
