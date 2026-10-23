package polls

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRelaysLive reads the default relays (NOSTR_LIVE=1, with DATABASE_URL
// for the cursors).
func TestRelaysLive(t *testing.T) {
	if os.Getenv("NOSTR_LIVE") == "" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("NOSTR_LIVE or DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box := NewPGStore(pool)
	s := &Service{store: box, box: box}
	for _, r := range DefaultRelays {
		n, err := s.readRelay(context.Background(), r)
		t.Logf("%s: %d events, err %v", r, n, err)
		if err != nil {
			t.Error(r, err)
		}
	}
}
