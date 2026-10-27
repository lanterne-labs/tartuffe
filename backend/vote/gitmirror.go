package vote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// GitMirror is a Mirror on a git repository: the files are staged in a
// working clone and each checkpoint is one commit, pushed to the remote;
// the token is the commit's hash, which the browser reads through a CDN
// (jsDelivr: cdn.jsdelivr.net/gh/<owner>/<repo>@<commit>/<path>, immutable)
// and the latest files through the repository's pages. The remote carries
// its credentials (a token in the URL, or a deploy key of the machine).
type GitMirror struct {
	dir    *DirMirror
	remote string
	branch string
	sshKey string // a private key file for an ssh remote, "" for none
}

// NewGitMirror opens the clone in dir, cloning the remote's branch when
// dir is empty (an empty remote gets its first commit at the first
// Commit). An ssh remote (git@github.com:org/repo.git) authenticates with
// the deploy key in VOTE_MIRROR_SSH_KEY (the private key's text), written
// to a file of the clone's parent directory.
func NewGitMirror(ctx context.Context, dir, remote, branch string) (*GitMirror, error) {
	if branch == "" {
		branch = "main"
	}
	m := &GitMirror{dir: &DirMirror{Dir: dir}, remote: remote, branch: branch}
	if key := os.Getenv("VOTE_MIRROR_SSH_KEY"); key != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.Clean(dir)), 0o755); err != nil {
			return nil, err
		}
		m.sshKey = filepath.Join(filepath.Dir(filepath.Clean(dir)), ".mirror-deploy-key")
		if err := os.WriteFile(m.sshKey, []byte(strings.TrimSpace(key)+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("mirror ssh key: %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if out, err := m.git(ctx, "pull", "--ff-only", "-q", "origin", branch); err != nil && !strings.Contains(out, "couldn't find remote ref") {
			return nil, fmt.Errorf("git pull: %s: %w", strings.TrimSpace(out), err)
		}
		return m, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out, err := m.git(ctx, "clone", "-q", "--branch", branch, "--single-branch", remote, ".")
	if err != nil {
		if !strings.Contains(out, "Remote branch") && !strings.Contains(out, "not found") && !strings.Contains(out, "empty repository") {
			return nil, fmt.Errorf("git clone: %s: %w", strings.TrimSpace(out), err)
		}
		// An empty remote, or one without the branch: start it.
		for _, args := range [][]string{{"init", "-q", "-b", branch}, {"remote", "add", "origin", remote}} {
			if out, err := m.git(ctx, args...); err != nil {
				return nil, fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(out), err)
			}
		}
	}
	return m, nil
}

func (m *GitMirror) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = m.dir.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=tartuffe-vote", "GIT_AUTHOR_EMAIL=vote@tartuffe.me", "GIT_COMMITTER_NAME=tartuffe-vote", "GIT_COMMITTER_EMAIL=vote@tartuffe.me")
	if m.sshKey != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -i "+m.sshKey+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Read implements Mirror.
func (m *GitMirror) Read(path string) ([]byte, error) { return m.dir.Read(path) }

// Stage implements Mirror.
func (m *GitMirror) Stage(path string, data []byte) error { return m.dir.Stage(path, data) }

// Commit implements Mirror: one commit of everything staged, pushed; the
// token is its hash. Nothing staged gives the current head.
func (m *GitMirror) Commit(ctx context.Context, message string) (string, error) {
	m.dir.pending = nil
	if out, err := m.git(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
	}
	var exit *exec.ExitError
	if out, err := m.git(ctx, "diff", "--cached", "--quiet"); err == nil {
		return m.head(ctx)
	} else if !errors.As(err, &exit) {
		return "", fmt.Errorf("git diff: %s: %w", strings.TrimSpace(out), err)
	}
	if out, err := m.git(ctx, "commit", "-q", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %s: %w", strings.TrimSpace(out), err)
	}
	token, err := m.head(ctx)
	if err != nil {
		return "", err
	}
	if out, err := m.git(ctx, "push", "-q", "origin", m.branch); err != nil {
		// The commit stands; the next push carries it. A browser that
		// reads this token meanwhile falls back to the latest files.
		log.Error().Err(err).Str("out", strings.TrimSpace(out)).Msg("vote: mirror push")
	}
	return token, nil
}

func (m *GitMirror) head(ctx context.Context) (string, error) {
	out, err := m.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %s: %w", strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out), nil
}
