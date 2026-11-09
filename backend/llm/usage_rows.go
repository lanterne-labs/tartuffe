package llm

import "sort"

// UsageRow is the usage of one task on one model at one endpoint since the
// start of the process: what UsageReport prints, as numbers.
type UsageRow struct {
	Endpoint EndpointName
	Task     Task
	Model    string
	TaskUsage
}

// usageEndpoint is an endpoint that counts its usage per task and model.
type usageEndpoint interface {
	usageRows() []UsageRow
}

// UsageRows returns the usage of every endpoint of the active routing, per
// task and model, since the start of the process; sorted by endpoint, task
// and model. The caller keeps the last rows to get the usage of a period.
func UsageRows() []UsageRow {
	r := activeRouting()
	if r == nil {
		return nil
	}
	var out []UsageRow
	for name, e := range r.Endpoints {
		u, ok := e.(usageEndpoint)
		if !ok {
			continue
		}
		for _, row := range u.usageRows() {
			row.Endpoint = name
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Endpoint != out[j].Endpoint {
			return out[i].Endpoint < out[j].Endpoint
		}
		if out[i].Task != out[j].Task {
			return out[i].Task < out[j].Task
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// usageRowsOf copies usage as rows, without the endpoint. The caller holds
// the lock of usage.
func usageRowsOf(usage map[usageKey]*TaskUsage) []UsageRow {
	out := make([]UsageRow, 0, len(usage))
	for key, u := range usage {
		out = append(out, UsageRow{Task: key.task, Model: key.model, TaskUsage: *u})
	}
	return out
}
