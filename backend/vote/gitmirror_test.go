package vote

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A git mirror on a bare repository: each commit is pushed and its hash
// is the token; a second clone reads the files at that commit; a commit
// with nothing staged keeps the head.
func TestGitMirror(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	m, err := NewGitMirror(ctx, filepath.Join(root, "work"), bare, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stage("checkpoint.txt", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := m.Stage("entries/2026-10-27T00.json", []byte("[]")); err != nil {
		t.Fatal(err)
	}
	t1, err := m.Commit(ctx, "checkpoint 1")
	if err != nil {
		t.Fatal(err)
	}
	if len(t1) != 40 {
		t.Fatalf("token %q", t1)
	}
	again, err := m.Commit(ctx, "nothing")
	if err != nil || again != t1 {
		t.Fatalf("empty commit: %s %v", again, err)
	}
	_ = m.Stage("checkpoint.txt", []byte("two\n"))
	t2, err := m.Commit(ctx, "checkpoint 2")
	if err != nil || t2 == t1 {
		t.Fatalf("second commit: %s %v", t2, err)
	}
	// The remote has both; a fresh clone reads the first commit's file.
	out, err := exec.Command("git", "-C", bare, "show", t1+":checkpoint.txt").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "one" {
		t.Fatalf("remote at %s: %s %v", t1[:8], out, err)
	}
	out, _ = exec.Command("git", "-C", bare, "show", t2+":checkpoint.txt").CombinedOutput()
	if strings.TrimSpace(string(out)) != "two" {
		t.Fatalf("remote at %s: %s", t2[:8], out)
	}
	// Reopening the clone pulls and keeps the files.
	m2, err := NewGitMirror(ctx, filepath.Join(root, "work"), bare, "main")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := m2.Read("checkpoint.txt"); string(b) != "two\n" {
		t.Fatalf("reopened: %q", b)
	}
	// A clone from scratch of the remote.
	m3, err := NewGitMirror(ctx, filepath.Join(root, "other"), bare, "main")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := m3.Read("entries/2026-10-27T00.json"); string(b) != "[]" {
		t.Fatalf("clone: %q", b)
	}
}
