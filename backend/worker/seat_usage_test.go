package worker

import (
	"testing"

	"digital-mirror/backend/llm/seatreserve"
)

func TestSeatUsageRows(t *testing.T) {
	rows := seatUsageRows(map[string]seatreserve.Usage{
		"a": {FiveHour: &seatreserve.Window{Percent: 12}, SevenDay: &seatreserve.Window{Percent: 40}},
		"b": {SevenDay: &seatreserve.Window{Percent: 91}},
	})
	got := map[string]float64{}
	for _, r := range rows {
		if r.JobType != seatUsageJob {
			t.Fatalf("job %q", r.JobType)
		}
		got[r.Target+"/"+r.Metric] = r.Value
	}
	want := map[string]float64{"a/five_hour_percent": 12, "a/seven_day_percent": 40, "b/seven_day_percent": 91}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %v, want %v", k, got[k], v)
		}
	}
}
