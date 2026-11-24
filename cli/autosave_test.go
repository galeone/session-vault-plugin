package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func withConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SESSION_VAULT_CONFIG", path)
	t.Setenv(autosaveKey, "")
	return path
}

func TestAutosaveSetting(t *testing.T) {
	cases := []struct {
		file, env string
		want      bool
		wantErr   bool
	}{
		{file: "", want: true},
		{file: "SESSION_VAULT_URL=x\n", want: true},
		{file: "SESSION_VAULT_AUTOSAVE=off\n", want: false},
		{file: "export SESSION_VAULT_AUTOSAVE=\"false\"\n", want: false},
		{file: "SESSION_VAULT_AUTOSAVE=off\nSESSION_VAULT_AUTOSAVE=on\n", want: true},
		{file: "# SESSION_VAULT_AUTOSAVE=off\n", want: true},
		{file: "SESSION_VAULT_AUTOSAVE=off\n", env: "on", want: true},
		{file: "", env: "0", want: false},
		{file: "SESSION_VAULT_AUTOSAVE=sometimes\n", wantErr: true},
	}
	for _, c := range cases {
		withConfigFile(t, c.file)
		t.Setenv(autosaveKey, c.env)
		got, _, err := autosaveSetting()
		if (err != nil) != c.wantErr || (!c.wantErr && got != c.want) {
			t.Errorf("file %q env %q: got %v, %v; want %v (err %v)", c.file, c.env, got, err, c.want, c.wantErr)
		}
	}
}

func TestSetAutosave(t *testing.T) {
	path := withConfigFile(t, "SESSION_VAULT_URL=https://x\n# keep me\nSESSION_VAULT_AUTOSAVE=on\nSESSION_VAULT_TOKEN=tok\nSESSION_VAULT_AUTOSAVE=on\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := setAutosave("off"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	want := "SESSION_VAULT_URL=https://x\n# keep me\nSESSION_VAULT_AUTOSAVE=off\nSESSION_VAULT_TOKEN=tok\n"
	if string(b) != want {
		t.Fatalf("config =\n%s\nwant\n%s", b, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", info.Mode().Perm())
	}
	if on, _, _ := autosaveSetting(); on {
		t.Error("still on after autosave off")
	}

	// No config file yet: created 0600.
	path = withConfigFile(t, "")
	if err := setAutosave("on"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "SESSION_VAULT_AUTOSAVE=on\n" {
		t.Errorf("new config = %q", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("new config mode %v, want 0600", info.Mode().Perm())
	}
	if err := setAutosave("maybe"); err == nil {
		t.Error("invalid value accepted")
	}
}

// stubDetached records background saves instead of starting them.
func stubDetached(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := startDetached
	startDetached = func(dir string, args ...string) error {
		calls = append(calls, append([]string{dir}, args...))
		return nil
	}
	t.Cleanup(func() { startDetached = orig })
	return &calls
}

func TestSessionEnd(t *testing.T) {
	cwd := t.TempDir()
	input := `{"session_id":"s1","cwd":"` + cwd + `","reason":"prompt_input_exit","hook_event_name":"SessionEnd"}`

	t.Run("starts a background save", func(t *testing.T) {
		withConfigFile(t, "SESSION_VAULT_URL=https://x\nSESSION_VAULT_TOKEN=tok\n")
		calls := stubDetached(t)
		if err := sessionEnd(strings.NewReader(input)); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || strings.Join((*calls)[0], " ") != cwd+" autosave-run s1" {
			t.Fatalf("calls = %v", *calls)
		}
	})
	t.Run("off", func(t *testing.T) {
		withConfigFile(t, "SESSION_VAULT_URL=https://x\nSESSION_VAULT_TOKEN=tok\nSESSION_VAULT_AUTOSAVE=off\n")
		calls := stubDetached(t)
		_ = sessionEnd(strings.NewReader(input))
		if len(*calls) != 0 {
			t.Fatalf("autosave off still saved: %v", *calls)
		}
	})
	t.Run("not configured", func(t *testing.T) {
		withConfigFile(t, "")
		t.Setenv("SESSION_VAULT_URL", "")
		t.Setenv("SESSION_VAULT_TOKEN", "")
		calls := stubDetached(t)
		_ = sessionEnd(strings.NewReader(input))
		if len(*calls) != 0 {
			t.Fatalf("unconfigured plugin saved: %v", *calls)
		}
	})
	t.Run("bad input", func(t *testing.T) {
		withConfigFile(t, "SESSION_VAULT_URL=https://x\nSESSION_VAULT_TOKEN=tok\n")
		calls := stubDetached(t)
		for _, in := range []string{"", "not json", `{"session_id":"../x"}`, `{"cwd":"/tmp"}`} {
			if err := sessionEnd(strings.NewReader(in)); err != nil {
				t.Errorf("%q: hook failed: %v", in, err)
			}
		}
		if len(*calls) != 0 {
			t.Fatalf("bad input saved: %v", *calls)
		}
	})
}

const chatTranscript = `{"type":"user","cwd":"/work/repo","message":{"content":"hi"}}
{"type":"assistant","cwd":"/work/repo","message":{"content":"hello"}}
`

func setupAutosave(t *testing.T, transcript string) (*memVault, string) {
	t.Helper()
	autosaveSettle = 0
	v, _, _ := setupRotation(t)
	writeSession(t, "s1", nil)
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-work-repo", "s1.jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	return v, path
}

func TestAutosaveRun(t *testing.T) {
	v, path := setupAutosave(t, chatTranscript)
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	first, ok := v.metas["s1"]
	if !ok {
		t.Fatal("session not saved")
	}

	// Unchanged since that save: skipped.
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if !v.metas["s1"].UploadedAt.Equal(first.UploadedAt) {
		t.Error("an unchanged session was uploaded again")
	}

	// Changed afterwards: saved again.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if !v.metas["s1"].UploadedAt.After(first.UploadedAt) {
		t.Error("a changed session was not saved again")
	}
}

func TestAutosaveRunSkipsEmptySession(t *testing.T) {
	v, _ := setupAutosave(t, `{"type":"user","message":{"content":"/model"}}`+"\n")
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.metas["s1"]; ok {
		t.Error("a session without a reply from Claude was saved")
	}
}

func TestAutosaveRunNeverCreatesAKey(t *testing.T) {
	v, _ := setupAutosave(t, chatTranscript)
	missing := filepath.Join(t.TempDir(), "age.key")
	t.Setenv("SESSION_VAULT_AGE_KEY", missing)
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.metas["s1"]; ok {
		t.Error("saved without a key")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("autosave generated a key")
	}
}

func TestAutosaveRunKeepsNewerServerCopy(t *testing.T) {
	v, path := setupAutosave(t, chatTranscript)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	// Saved from another machine after this copy was last written.
	other, _ := age.GenerateX25519Identity()
	v.put(t, "s1", "other-machine", "newer", other.Recipient())
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if v.metas["s1"].Name != "other-machine" {
		t.Error("the newer server copy was overwritten")
	}
}

func TestAutosaveLimitNotice(t *testing.T) {
	v, _ := setupAutosave(t, chatTranscript)
	v.limit = 1
	other, _ := age.GenerateX25519Identity()
	v.put(t, "already-there", "", "x", other.Recipient())
	projects := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-work-repo")
	for _, id := range []string{"s1", "s2"} {
		if err := os.WriteFile(filepath.Join(projects, id+".jsonl"), []byte(chatTranscript), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := autosaveRun(id); err == nil {
			t.Fatalf("%s saved past the limit", id)
		}
	}

	var out bytes.Buffer
	printLimitNotice(&out)
	var hook struct {
		SystemMessage string `json:"systemMessage"`
	}
	if err := json.Unmarshal(out.Bytes(), &hook); err != nil {
		t.Fatalf("notice is not hook JSON: %q (%v)", out.String(), err)
	}
	for _, want := range []string{"2 sessions weren't saved", "free plan's limit of 1", "https://vault.example/account", "/session-vault:autosave off"} {
		if !strings.Contains(hook.SystemMessage, want) {
			t.Errorf("notice %q lacks %q", hook.SystemMessage, want)
		}
	}

	out.Reset()
	printLimitNotice(&out)
	if out.Len() != 0 {
		t.Errorf("the notice was shown twice: %q", out.String())
	}
}

func TestNoLimitNoticeOnOtherFailures(t *testing.T) {
	v, _ := setupAutosave(t, chatTranscript)
	v.failPost["s1"] = 1
	_ = autosaveRun("s1")
	var out bytes.Buffer
	printLimitNotice(&out)
	if out.Len() != 0 {
		t.Errorf("a 500 produced a limit notice: %q", out.String())
	}
}

const titledTranscript = `{"type":"user","cwd":"/work/repo","message":{"content":"hi"}}
{"type":"assistant","cwd":"/work/repo","message":{"content":"hello"}}
{"type":"ai-title","aiTitle":"LinkedIn post draft"}
`

func TestSaveKeepsSavedName(t *testing.T) {
	v, path := setupAutosave(t, titledTranscript)

	// Not on the server yet: named after the title.
	if err := save("", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Name; got != "linkedin-post-draft" {
		t.Fatalf("first save named %q, want the title", got)
	}

	// Renamed by the user, then the title changes: unnamed saves (manual or
	// automatic) keep the user's name.
	if err := save("advertising", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(titledTranscript+`{"type":"ai-title","aiTitle":"Something else"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := save("", "s1"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(path, later, later)
	if err := autosaveRun("s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Name; got != "advertising" {
		t.Fatalf("unnamed re-save renamed the session to %q", got)
	}

	// An explicit name still replaces it.
	if err := save("renamed", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Name; got != "renamed" {
		t.Fatalf("explicit name gave %q", got)
	}
}
