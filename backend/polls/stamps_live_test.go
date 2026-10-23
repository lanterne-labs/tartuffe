package polls

import (
	"context"
	"os"
	"testing"
)

// TestOTSLive writes a real .ots file (OTS_LIVE=dir) for the ots client.
func TestOTSLive(t *testing.T) {
	dir := os.Getenv("OTS_LIVE")
	if dir == "" {
		t.Skip("OTS_LIVE not set")
	}
	msg := []byte("tartuffe.me/vote-log\n1\nAAAA\n\n— test\n")
	ots, err := otsFile(context.Background(), CheckpointDigest(msg))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(dir+"/cp.txt", msg, 0o644)
	os.WriteFile(dir+"/cp.txt.ots", ots, 0o644)
}
