package frpolitics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUnmarshalSampleScrutin(t *testing.T) {
	p := filepath.Join("./asset", "sample_scrutin.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	var s scrutinJSON
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal scrutin: %v", err)
	}
	if s.Scrutin.Legislature == 0 {
		t.Fatalf("expected legislature > 0")
	}
	if int(s.Scrutin.Numero) == 0 {
		t.Fatalf("expected numero > 0")
	}
}

