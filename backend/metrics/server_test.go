package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// The mux serves the counters of the process, so Prometheus reads a run of
// the daily pipeline while it lasts.
func TestNewServeMuxServesTheRegistry(t *testing.T) {
	IncrementFactsExtracted("news", true)
	srv := httptest.NewServer(NewServeMux())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(body), "digital_mirror_facts_extracted_total") {
		t.Fatalf("status %d; want the fact counter in the body", resp.StatusCode)
	}
}
