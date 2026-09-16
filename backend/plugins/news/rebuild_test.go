package news

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRebuildLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	lines := []string{
		`{"ts":"2026-01-01T00:00:00Z","person":"Alice","url":"https://a/1","facts_extracted":2}`,
		`{"ts":"2026-01-01T00:00:01Z","person":"Alice","url":"https://a/1","facts_extracted":0}`, // duplicate pair
		`{"ts":"2026-01-01T00:00:02Z","person":"Alice","url":"","errors":["delete: boom"]}`,      // no url
		`not json at all`,
		`{"ts":"2026-01-01T00:00:03Z","errors":["orphan-sweep: deleted 3 facts, 1 stances cleaned"]}`, // sweep line
		`{"ts":"2026-01-01T00:00:04Z","person":"Bob","url":"https://b/1"}`,
		`{"ts":"2026-01-01T00:00:05Z","person":"Alice","url":"https://a/2"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRebuildLog(path)
	if err != nil {
		t.Fatalf("ReadRebuildLog: %v", err)
	}
	want := []RebuildLogPair{
		{Person: "Alice", URL: "https://a/1"},
		{Person: "Bob", URL: "https://b/1"},
		{Person: "Alice", URL: "https://a/2"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pair %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if _, err := ReadRebuildLog(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("expected an error for a missing log file")
	}
}

func TestRebuildCheckpointRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())

	if _, err := loadRebuildCheckpoint(); err == nil {
		t.Fatal("expected an error when no checkpoint exists")
	}

	cp := rebuildCheckpoint{DonePersonURL: map[string]bool{"Alice|https://a/1": true}}
	if err := saveRebuildCheckpoint(cp); err != nil {
		t.Fatalf("saveRebuildCheckpoint: %v", err)
	}
	loaded, err := loadRebuildCheckpoint()
	if err != nil {
		t.Fatalf("loadRebuildCheckpoint: %v", err)
	}
	if !loaded.DonePersonURL["Alice|https://a/1"] {
		t.Errorf("checkpoint lost the done pair: %+v", loaded)
	}

	// An empty object on disk must still yield a usable map.
	if err := os.WriteFile(RebuildCheckpointFile, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadRebuildCheckpoint()
	if err != nil {
		t.Fatalf("loadRebuildCheckpoint on empty object: %v", err)
	}
	if loaded.DonePersonURL == nil {
		t.Error("DonePersonURL should be initialised when absent from the file")
	}
}
