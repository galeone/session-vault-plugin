package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

// Upload schemes (the X-Encrypted header). schemeTranscript is
// age(gzip(transcript)); schemeBundle is age(gzip(tar)) holding the
// transcript and the subagent transcripts Claude Code keeps next to it.
// The bundle has its own name so a client that predates it refuses it
// ("update the plugin") instead of writing a tar archive as a transcript.
const (
	schemeTranscript = "age+gzip"
	schemeBundle     = "age+gzip+tar"
)

// Entry names inside a bundle. They are fixed, not taken from the session
// id, so a restore never builds a path from archive content beyond a
// validated file name.
const (
	bundleTranscript  = "transcript.jsonl"
	bundleSubagentDir = "subagents/"
)

// validSubagentFile is what a subagent file name must look like, both when
// saving and, more importantly, when a bundle from the server is restored.
// Claude Code names them agent-<hex>.jsonl and agent-<hex>.meta.json.
var validSubagentFile = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// subagentDir is where Claude Code keeps the subagent transcripts of the
// session whose transcript is at transcriptPath:
// <project>/<session-id>/subagents/.
func subagentDir(transcriptPath string) string {
	return filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "subagents")
}

// subagentFiles lists the regular files in the session's subagents
// directory, sorted. Anything else (sub-directories, symlinks, odd names)
// is left out: only what a restore would accept is saved.
func subagentFiles(transcriptPath string) ([]string, error) {
	dir := subagentDir(transcriptPath)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() && validSubagentFile.MatchString(e.Name()) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

// countTranscripts counts the .jsonl files among paths: each subagent has
// a transcript and a .meta.json next to it.
func countTranscripts(paths []string) int {
	n := 0
	for _, p := range paths {
		if strings.HasSuffix(p, ".jsonl") {
			n++
		}
	}
	return n
}

// encodeSession streams the upload body for a transcript and returns its
// scheme: the plain transcript when there are no subagent files (readable
// by every client version), a bundle otherwise.
func encodeSession(transcript string, subagents []string, recipients []age.Recipient) (io.Reader, string, error) {
	if len(subagents) == 0 {
		r, err := encodeTranscript(transcript, recipients)
		return r, schemeTranscript, err
	}
	if len(recipients) == 0 {
		return nil, "", fmt.Errorf("no age recipient: refusing to upload unencrypted")
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(writeBundle(pw, transcript, subagents, recipients))
	}()
	return pr, schemeBundle, nil
}

func writeBundle(w io.Writer, transcript string, subagents []string, recipients []age.Recipient) error {
	enc, err := age.Encrypt(w, recipients...)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	if err := addTarFile(tw, bundleTranscript, transcript); err != nil {
		return err
	}
	for _, f := range subagents {
		if err := addTarFile(tw, bundleSubagentDir+filepath.Base(f), f); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return enc.Close()
}

// addTarFile adds path under name. The size is taken from the bytes read,
// not from a stat, so a file Claude Code appends to meanwhile is stored
// consistently (as of the moment it was read).
func addTarFile(tw *tar.Writer, name, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

// restoredFile is one file of a restored session, decrypted into a temp
// file in its destination directory (so the final rename never crosses
// file systems).
type restoredFile struct {
	tmp  string
	dest string
}

// unpackBundle reads a decrypted, decompressed bundle and writes its files
// to temp files: the transcript into destDir, the subagent files into
// <destDir>/<id>/subagents/. The caller renames them into place or removes
// them. A bundle without a transcript, with an unexpected entry name or
// type, or with a repeated entry is refused.
func unpackBundle(r io.Reader, destDir, id string) (transcript restoredFile, subagents []restoredFile, err error) {
	cleanup := func() {
		if transcript.tmp != "" {
			_ = os.Remove(transcript.tmp)
		}
		for _, f := range subagents {
			_ = os.Remove(f.tmp)
		}
	}
	defer func() {
		if err != nil {
			cleanup()
			transcript, subagents = restoredFile{}, nil
		}
	}()
	subDir := filepath.Join(destDir, id, "subagents")
	seen := map[string]bool{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return transcript, subagents, fmt.Errorf("reading the bundle: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return transcript, subagents, fmt.Errorf("unexpected entry %q in the bundle", hdr.Name)
		}
		if seen[hdr.Name] {
			return transcript, subagents, fmt.Errorf("repeated entry %q in the bundle", hdr.Name)
		}
		seen[hdr.Name] = true
		switch {
		case hdr.Name == bundleTranscript:
			tmp, err := writeTemp(destDir, "."+id+".*", tr)
			if err != nil {
				return transcript, subagents, err
			}
			transcript = restoredFile{tmp: tmp, dest: filepath.Join(destDir, id+".jsonl")}
		case strings.HasPrefix(hdr.Name, bundleSubagentDir) && validSubagentFile.MatchString(strings.TrimPrefix(hdr.Name, bundleSubagentDir)):
			name := strings.TrimPrefix(hdr.Name, bundleSubagentDir)
			if err := os.MkdirAll(subDir, 0o755); err != nil {
				return transcript, subagents, err
			}
			tmp, err := writeTemp(subDir, "."+name+".*", tr)
			if err != nil {
				return transcript, subagents, err
			}
			subagents = append(subagents, restoredFile{tmp: tmp, dest: filepath.Join(subDir, name)})
		default:
			return transcript, subagents, fmt.Errorf("unexpected entry %q in the bundle", hdr.Name)
		}
	}
	if transcript.tmp == "" {
		return transcript, subagents, fmt.Errorf("the bundle has no transcript")
	}
	return transcript, subagents, nil
}

func writeTemp(dir, pattern string, r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// placeSubagents moves restored subagent files into place. A file that is
// identical locally is left alone; one that differs is kept as a .bak
// before it is replaced, like the transcript. It returns how many files
// it wrote.
func placeSubagents(files []restoredFile) (int, error) {
	written := 0
	for _, f := range files {
		if _, err := os.Stat(f.dest); err == nil {
			same, err := sameContent(f.tmp, f.dest)
			if err != nil {
				return written, err
			}
			if same {
				_ = os.Remove(f.tmp)
				continue
			}
			backup := f.dest + "." + time.Now().UTC().Format("20060102T150405Z") + ".bak"
			if err := os.Rename(f.dest, backup); err != nil {
				return written, fmt.Errorf("backing up %s: %w", f.dest, err)
			}
		}
		if err := os.Rename(f.tmp, f.dest); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// subagentsCurrent reports whether every restored subagent file is already
// present locally with the same content.
func subagentsCurrent(files []restoredFile) (bool, error) {
	for _, f := range files {
		if _, err := os.Stat(f.dest); os.IsNotExist(err) {
			return false, nil
		}
		same, err := sameContent(f.tmp, f.dest)
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}
