package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
)

// writeSession lays out a Claude Code session with subagent files under a
// fresh CLAUDE_CONFIG_DIR and returns the projects dir.
func writeSession(t *testing.T, id string, subagents map[string]string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proj := filepath.Join(base, "projects", "-work-repo")
	if err := os.MkdirAll(filepath.Join(proj, id, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(`{"cwd":"/work/repo"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range subagents {
		if err := os.WriteFile(filepath.Join(proj, id, "subagents", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(base, "projects")
}

// moveConfigDir points CLAUDE_CONFIG_DIR at an empty dir, as on a second
// machine, and returns its projects dir.
func moveConfigDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", base)
	return filepath.Join(base, "projects")
}

func TestSaveAndFetchSubagents(t *testing.T) {
	v, _, _ := setupRotation(t)
	subs := map[string]string{"agent-a1.jsonl": `{"sub":1}`, "agent-a1.meta.json": `{"agentType":"Explore"}`}
	writeSession(t, "s1", subs)
	// A directory and a hidden file next to them are not saved.
	src := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-work-repo", "s1", "subagents")
	_ = os.Mkdir(filepath.Join(src, "nested"), 0o755)
	_ = os.WriteFile(filepath.Join(src, ".hidden"), []byte("x"), 0o600)

	if err := save("", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Encrypted; got != schemeBundle {
		t.Fatalf("stored scheme %q, want %s", got, schemeBundle)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	projects := moveConfigDir(t)
	written, err := download(cfg, v.metas["s1"], "-here")
	if err != nil || !written {
		t.Fatalf("download = %v, %v", written, err)
	}
	if b, _ := os.ReadFile(filepath.Join(projects, "-here", "s1.jsonl")); string(b) != `{"cwd":"/work/repo"}`+"\n" {
		t.Errorf("transcript = %q", b)
	}
	got, _ := os.ReadDir(filepath.Join(projects, "-here", "s1", "subagents"))
	if len(got) != len(subs) {
		t.Fatalf("restored %d subagent files, want %d", len(got), len(subs))
	}
	for name, want := range subs {
		if b, _ := os.ReadFile(filepath.Join(projects, "-here", "s1", "subagents", name)); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}

	// A newer save where only a subagent changed updates that file alone.
	cfgRecs, _ := cfg.recipients()
	v.putBundle(t, "s1", `{"cwd":"/work/repo"}`+"\n", map[string]string{"agent-a1.jsonl": `{"sub":2}`, "agent-a1.meta.json": `{"agentType":"Explore"}`}, cfgRecs...)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(projects, "-here", "s1.jsonl"), past, past)
	if _, err := download(cfg, v.metas["s1"], "-here"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(projects, "-here", "s1", "subagents", "agent-a1.jsonl")); string(b) != `{"sub":2}` {
		t.Errorf("changed subagent file = %q", b)
	}
	baks, _ := filepath.Glob(filepath.Join(projects, "-here", "s1", "subagents", "*.bak"))
	if len(baks) != 1 {
		t.Errorf("want one subagent .bak, got %v", baks)
	}
	if baks, _ := filepath.Glob(filepath.Join(projects, "-here", "s1.jsonl.*.bak")); len(baks) != 0 {
		t.Errorf("unchanged transcript was backed up: %v", baks)
	}
}

func TestSaveWithoutSubagentsStaysPlain(t *testing.T) {
	v, _, _ := setupRotation(t)
	writeSession(t, "s1", nil)
	if err := save("", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Encrypted; got != schemeTranscript {
		t.Fatalf("stored scheme %q, want %s", got, schemeTranscript)
	}
}

func TestSaveFallsBackOnOldServer(t *testing.T) {
	v, _, _ := setupRotation(t)
	v.plainOnly = true
	writeSession(t, "s1", map[string]string{"agent-a1.jsonl": `{}`})
	if err := save("", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Encrypted; got != schemeTranscript {
		t.Fatalf("stored scheme %q, want the plain fallback %s", got, schemeTranscript)
	}
}

func TestRotateKeyKeepsBundleScheme(t *testing.T) {
	v, keyPath, old := setupRotation(t)
	v.putBundle(t, "s1", "{}\n", map[string]string{"agent-a1.jsonl": `{"sub":1}`}, old.Recipient())
	if err := rotateKey(); err != nil {
		t.Fatal(err)
	}
	if got := v.metas["s1"].Encrypted; got != schemeBundle {
		t.Fatalf("scheme after rotation %q, want %s", got, schemeBundle)
	}
	ids := keyFileIdentities(t, keyPath)
	if _, err := v.open("s1", ids...); err != nil {
		t.Fatalf("bundle not readable with the new key: %v", err)
	}
}

func TestUnpackBundleRefusesBadEntries(t *testing.T) {
	cases := map[string][]tar.Header{
		"traversal":     {{Name: bundleTranscript}, {Name: "subagents/../../evil"}},
		"absolute":      {{Name: bundleTranscript}, {Name: "/etc/passwd"}},
		"dotfile":       {{Name: bundleTranscript}, {Name: "subagents/.."}},
		"unknown":       {{Name: bundleTranscript}, {Name: "other.jsonl"}},
		"symlink":       {{Name: bundleTranscript}, {Name: "subagents/x", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		"repeated":      {{Name: bundleTranscript}, {Name: bundleTranscript}},
		"no transcript": {{Name: "subagents/agent-a.jsonl"}},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, h := range hdrs {
				h := h
				if h.Typeflag == 0 {
					h.Typeflag = tar.TypeReg
					h.Size = 2
				}
				if err := tw.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
				if h.Typeflag == tar.TypeReg {
					_, _ = tw.Write([]byte("{}"))
				}
			}
			_ = tw.Close()
			dir := t.TempDir()
			if _, _, err := unpackBundle(&buf, dir, "s1"); err == nil {
				t.Fatal("bad bundle accepted")
			}
			var left []string
			_ = filepath.Walk(dir, func(p string, info os.FileInfo, _ error) error {
				if !info.IsDir() {
					left = append(left, p)
				}
				return nil
			})
			if len(left) != 0 {
				t.Fatalf("refused bundle left files: %v", left)
			}
		})
	}
}

// putBundle stores a bundle encrypted to recs, as save would.
func (v *memVault) putBundle(t *testing.T, id, transcript string, subs map[string]string, recs ...age.Recipient) {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	add := func(name, content string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(content))
	}
	add(bundleTranscript, transcript)
	for name, c := range subs {
		add(bundleSubagentDir+name, c)
	}
	_ = tw.Close()
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(tb.Bytes())
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
	v.metas[id] = Meta{SessionID: id, Project: "-work-repo", Cwd: "/work/repo",
		Size: int64(out.Len()), UploadedAt: time.Now(), Encrypted: schemeBundle}
	v.blobs[id] = out.Bytes()
}
