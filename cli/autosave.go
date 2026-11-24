package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Automatic saving: the plugin's SessionEnd hook runs `session-end`, which
// starts `autosave-run <id>` as a detached process and returns at once.
// Claude Code gives all SessionEnd hooks 1.5 seconds together (a plugin
// can't raise that), which an upload of a long session can't fit in, so the
// save itself runs on after Claude Code has exited. It never prints
// anything: results go to the log file.

const autosaveKey = "SESSION_VAULT_AUTOSAVE"

// autosaveSettle is how long autosave-run waits before reading the
// transcript, so whatever Claude Code writes while exiting is in it.
var autosaveSettle = 2 * time.Second

// startDetached starts the background save; replaced in tests.
var startDetached = func(dir string, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	// No stdio: the hook's pipes close when the hook returns, and the
	// child must not hold them open (or die writing to them).
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// autosaveSetting returns whether autosave is on and where that comes
// from: the environment, the config file, or the default (on).
func autosaveSetting() (on bool, source string, err error) {
	if v, ok := os.LookupEnv(autosaveKey); ok && strings.TrimSpace(v) != "" {
		on, err := parseOnOff(v)
		return on, "the " + autosaveKey + " environment variable", err
	}
	if v, ok := configFileValue(autosaveKey); ok {
		on, err := parseOnOff(v)
		return on, configPath(), err
	}
	return true, "the default", nil
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s=%q: want on or off", autosaveKey, v)
}

// configFileValue reads one KEY=VALUE from the config file (the last
// occurrence wins, like a shell sourcing it).
func configFileValue(key string) (string, bool) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return "", false
	}
	var value string
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, v, ok := strings.Cut(line, "=")
		if ok && !strings.HasPrefix(line, "#") && strings.TrimSpace(k) == key {
			value, found = strings.Trim(strings.TrimSpace(v), `"'`), true
		}
	}
	return value, found
}

// sessionEnd is the SessionEnd hook. It never fails the hook: anything
// that keeps it from saving is logged and skipped.
func sessionEnd(r io.Reader) error {
	var payload struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&payload); err != nil {
		slog.Debug("session-end: unreadable hook input", "error", err)
		return nil
	}
	if !validPathName.MatchString(payload.SessionID) {
		slog.Debug("session-end: no session id", "session_id", payload.SessionID)
		return nil
	}
	on, source, err := autosaveSetting()
	if err != nil {
		slog.Warn("session-end: autosave setting unreadable, not saving", "error", err)
		return nil
	}
	if !on {
		slog.Debug("session-end: autosave is off", "source", source)
		return nil
	}
	if _, err := loadConfig(); err != nil {
		// Not set up yet: nothing to save to.
		slog.Debug("session-end: not configured", "error", err)
		return nil
	}
	dir := payload.Cwd
	if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
		dir = ""
	}
	if err := startDetached(dir, "autosave-run", payload.SessionID); err != nil {
		slog.Error("session-end: could not start the background save", "session_id", payload.SessionID, "error", err)
		return nil
	}
	slog.Info("session-end: autosave started", "session_id", payload.SessionID, "reason", payload.Reason)
	return nil
}

// autosaveRun saves one session in the background. A session without a
// single reply from Claude (opened and closed, or only a slash command)
// isn't worth a slot in the user's quota and is skipped.
func autosaveRun(id string) error {
	time.Sleep(autosaveSettle)
	transcript, _, _, err := sessionByID(id)
	if err != nil {
		slog.Info("autosave: no transcript, skipped", "session_id", id, "error", err)
		return nil
	}
	if !hasAssistantTurn(transcript) {
		slog.Info("autosave: nothing to save yet, skipped", "session_id", id)
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		slog.Info("autosave: not configured, skipped", "error", err)
		return nil
	}
	// A manual save generates the key when there is none and tells the
	// user to copy it. Done silently in the background, on a machine the
	// user hasn't copied the key to yet, it would start a second key.
	recs, err := cfg.recipients()
	if err != nil || recs == nil {
		slog.Warn("autosave: no age key, skipped (run /session-vault:save once, or copy age.key here)", "key", cfg.AgeKey, "error", err)
		return nil
	}
	// Never replace a newer server copy (the session continued and was
	// saved on another machine) with this machine's older one; this also
	// skips a session that hasn't changed since it was last saved.
	if newer, err := serverCopyNewer(cfg, id, transcript); err != nil {
		slog.Error("autosave: listing sessions failed", "session_id", id, "error", err)
		return err
	} else if newer {
		slog.Info("autosave: the server copy is as new or newer, skipped", "session_id", id)
		return nil
	}
	if err := save("", id); err != nil {
		slog.Error("autosave failed", "session_id", id, "error", err)
		if limit, ok := sessionLimitError(err); ok {
			recordLimitNotice(limit)
		}
		return err
	}
	slog.Info("autosave: saved", "session_id", id)
	return nil
}

// serverCopyNewer reports whether the server holds a save of id made after
// the session's files (transcript and subagent files) were last written.
func serverCopyNewer(cfg config, id, transcript string) (bool, error) {
	metas, err := listSessions(cfg)
	if err != nil {
		return false, err
	}
	var uploaded time.Time
	for _, m := range metas {
		if m.SessionID == id && m.UploadedAt.After(uploaded) {
			uploaded = m.UploadedAt
		}
	}
	if uploaded.IsZero() {
		return false, nil
	}
	files := []string{transcript}
	if subs, err := subagentFiles(transcript); err == nil {
		files = append(files, subs...)
	}
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.ModTime().After(uploaded) {
			return false, nil
		}
	}
	return true, nil
}

// hasAssistantTurn reports whether the transcript holds at least one
// message from Claude. It looks only at each entry's type, nothing else.
func hasAssistantTurn(transcript string) bool {
	f, err := os.Open(transcript)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), []byte(`"assistant"`)) {
			continue
		}
		var line struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Type == "assistant" {
			return true
		}
	}
	return false
}

func autosaveStatus() error {
	on, source, err := autosaveSetting()
	if err != nil {
		return err
	}
	state := "on"
	if !on {
		state = "off"
	}
	fmt.Printf("autosave is %s (from %s)\n", state, source)
	if on {
		fmt.Println("every session is saved in the background when it ends; turn it off with: session-vault autosave off")
	} else {
		fmt.Println("sessions are saved only when you run /session-vault:save; turn autosave on with: session-vault autosave on")
	}
	return nil
}

// setAutosave writes SESSION_VAULT_AUTOSAVE to the config file, replacing
// an existing line and keeping everything else as it is.
func setAutosave(arg string) error {
	on, err := parseOnOff(arg)
	if err != nil {
		return fmt.Errorf("usage: session-vault autosave [on|off]")
	}
	value := "off"
	if on {
		value = "on"
	}
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	if len(data) > 0 {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	out := make([]string, 0, len(lines)+1)
	replaced := false
	for _, line := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if k, _, ok := strings.Cut(t, "="); ok && !strings.HasPrefix(t, "#") && strings.TrimSpace(k) == autosaveKey {
			if !replaced {
				out = append(out, autosaveKey+"="+value)
				replaced = true
			}
			continue // a later duplicate would win over ours: drop it
		}
		out = append(out, line)
	}
	if !replaced {
		out = append(out, autosaveKey+"="+value)
	}
	if err := writeConfigFile(path, []byte(strings.Join(out, "\n")+"\n")); err != nil {
		return err
	}
	fmt.Printf("autosave is now %s (%s)\n", value, path)
	if v, ok := os.LookupEnv(autosaveKey); ok && strings.TrimSpace(v) != "" {
		fmt.Printf("note: the %s environment variable (%q) overrides the config file\n", autosaveKey, v)
	}
	return nil
}

// writeConfigFile atomically replaces the config file. It holds the
// token, so a new file is 0600 and an existing one keeps its mode.
func writeConfigFile(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// limitReached is the server's 402 answer when a plan's session limit is
// reached.
type limitReached struct {
	Plan       string `json:"plan"`
	Limit      int    `json:"limit"`
	UpgradeURL string `json:"upgrade_url"`
}

func sessionLimitError(err error) (limitReached, bool) {
	var se *statusError
	if !errors.As(err, &se) || se.code != http.StatusPaymentRequired {
		return limitReached{}, false
	}
	var l limitReached
	_ = json.Unmarshal([]byte(se.body), &l) // best effort: the notice works without it
	return l, true
}

// The limit notice: a background save can't show anything, so a session
// it couldn't save because of the plan's limit is recorded here, and the
// next SessionStart hook (track) shows it to the user once.
type limitNotice struct {
	limitReached
	Unsaved int `json:"unsaved"`
}

func limitNoticePath() string {
	return filepath.Join(stateDir(), "autosave-limit-notice.json")
}

// recordLimitNotice adds one unsaved session to the pending notice.
func recordLimitNotice(l limitReached) {
	var n limitNotice
	if data, err := os.ReadFile(limitNoticePath()); err == nil {
		_ = json.Unmarshal(data, &n)
	}
	n.limitReached = l
	n.Unsaved++
	data, err := json.Marshal(n)
	if err != nil {
		return
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(limitNoticePath(), data, 0o600); err != nil {
		slog.Error("autosave: recording the limit notice failed", "error", err)
	}
}

// takeLimitNotice returns the pending notice's text and removes it, or ""
// when there is none.
func takeLimitNotice() string {
	data, err := os.ReadFile(limitNoticePath())
	if err != nil {
		return ""
	}
	_ = os.Remove(limitNoticePath())
	var n limitNotice
	if json.Unmarshal(data, &n) != nil || n.Unsaved == 0 {
		return ""
	}
	what := "your last session wasn't"
	if n.Unsaved > 1 {
		what = fmt.Sprintf("%d sessions weren't", n.Unsaved)
	}
	limit := "your plan's session limit is reached"
	if n.Limit > 0 && n.Plan != "" {
		limit = fmt.Sprintf("the %s plan's limit of %d sessions is reached", n.Plan, n.Limit)
	}
	msg := fmt.Sprintf("session-vault: %s saved automatically: %s.", what, limit)
	if n.UpgradeURL != "" {
		msg += " Upgrade at " + n.UpgradeURL + ","
	}
	msg += " delete old sessions with /session-vault:clean, or turn autosave off with /session-vault:autosave off."
	return msg
}

// printLimitNotice writes the pending notice, if any, as SessionStart hook
// JSON: systemMessage is shown to the user (plain stdout would only reach
// Claude's context).
func printLimitNotice(w io.Writer) {
	msg := takeLimitNotice()
	if msg == "" {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"systemMessage": msg})
}
