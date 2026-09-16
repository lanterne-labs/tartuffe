package llm

import "testing"

func TestParseJSONResponse(t *testing.T) {
	type verdict struct {
		Same      bool   `json:"same"`
		Reasoning string `json:"reasoning"`
	}
	for _, in := range []string{
		`{"same": true, "reasoning": "r"}`,
		"  \n```json\n{\"same\": true, \"reasoning\": \"r\"}\n```\n",
		"```\n{\"same\": true, \"reasoning\": \"r\"}\n```",
	} {
		v, err := ParseJSONResponse[verdict](in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !v.Same || v.Reasoning != "r" {
			t.Fatalf("%q: decoded %+v", in, v)
		}
	}
	if _, err := ParseJSONResponse[verdict]("not json"); err == nil {
		t.Fatal("invalid JSON must fail")
	}
	if _, err := ParseJSONResponse[verdict](""); err == nil {
		t.Fatal("empty content must fail")
	}
}
