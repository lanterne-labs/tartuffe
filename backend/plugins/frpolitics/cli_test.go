package frpolitics

import (
	"context"
	"strings"
	"testing"
)

// TestRunModeUnknown: an unknown mode is refused before anything runs, with
// the list of the modes.
func TestRunModeUnknown(t *testing.T) {
	err := RunMode(context.Background(), nil, CLIOptions{Mode: "nope"})
	if err == nil || !strings.Contains(err.Error(), "'recensure'") {
		t.Fatalf("err = %v, want the list of the modes", err)
	}
	if !strings.Contains(ModeHelp(), "'votes' (process official votes)") {
		t.Errorf("help = %q", ModeHelp())
	}
}
