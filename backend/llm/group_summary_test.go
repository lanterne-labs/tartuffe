package llm

import "testing"

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"summary":"a"}`, `{"summary":"a"}`},
		{"```json\n{\"summary\":\"a\"}\n```", `{"summary":"a"}`},
		{`Here you go: {"summary":"a","summary_en":"b"} done`, `{"summary":"a","summary_en":"b"}`},
		{`no json here`, ``},
		{`}{`, ``}, // closing before opening
	}
	for _, c := range cases {
		if got := extractJSONObject(c.in); got != c.want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
