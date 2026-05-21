package core

import (
	"testing"
	"time"
)

func TestPickPublishedAt(t *testing.T) {
	t1 := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		sources []*time.Time
		want    *time.Time
	}{
		{"no args", nil, nil},
		{"single nil", []*time.Time{nil}, nil},
		{"all nil", []*time.Time{nil, nil, nil}, nil},
		{"single non-nil", []*time.Time{&t1}, &t1},
		{"first wins", []*time.Time{&t1, &t2, &t3}, &t1},
		{"second wins when first nil", []*time.Time{nil, &t2, &t3}, &t2},
		{"third wins when first two nil", []*time.Time{nil, nil, &t3}, &t3},
		{"interleaved nils don't matter", []*time.Time{nil, &t2, nil, &t3}, &t2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PickPublishedAt(tc.sources...)
			if got != tc.want {
				gotStr := "<nil>"
				if got != nil {
					gotStr = got.Format(time.RFC3339)
				}
				wantStr := "<nil>"
				if tc.want != nil {
					wantStr = tc.want.Format(time.RFC3339)
				}
				t.Fatalf("got %s, want %s", gotStr, wantStr)
			}
		})
	}
}
