package vote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
)

// The audit of the mirror (SPEC §8.3, §12): the checkpoint note names the
// mirror commit that holds the files browsers read for it. The auditor
// reads those files at that commit and compares them with the log: the
// note itself, and the hour's entries file, which must list exactly the
// checkpoint's entries. A mirror that served browsers other files than the
// log's is caught here.

// MirrorReader reads a mirror file at a commit (the token of a note).
type MirrorReader interface {
	// ReadAt returns the file's content at the commit, nil when the file
	// does not exist there.
	ReadAt(ctx context.Context, commit, path string) ([]byte, error)
}

// GitMirrorReader reads a local clone of the mirror repository.
type GitMirrorReader struct{ Dir string }

// ReadAt implements MirrorReader with git show.
func (g GitMirrorReader) ReadAt(ctx context.Context, commit, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", g.Dir, "show", commit+":"+path)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (strings.Contains(string(ee.Stderr), "does not exist") || strings.Contains(string(ee.Stderr), "exists on disk, but not in")) {
			return nil, nil
		}
		return nil, fmt.Errorf("git show %s:%s: %w", commit, path, err)
	}
	return out, nil
}

// CDNMirrorReader reads the mirror repository through jsDelivr by commit,
// as browsers do.
type CDNMirrorReader struct {
	// Repo is "org/name".
	Repo   string
	Client *http.Client
}

// ReadAt implements MirrorReader.
func (c CDNMirrorReader) ReadAt(ctx context.Context, commit, path string) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	url := fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s@%s/%s", c.Repo, commit, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 50<<20))
}

// DirMirrorReader reads a directory mirror's current files, whatever the
// commit (tests and local trials: the directory mirror has no history).
type DirMirrorReader struct{ Dir string }

// ReadAt implements MirrorReader.
func (d DirMirrorReader) ReadAt(ctx context.Context, commit, path string) ([]byte, error) {
	return (&DirMirror{Dir: d.Dir}).Read(filepath.ToSlash(path))
}
