package metrics

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/golang/snappy"
	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog/log"
	"google.golang.org/protobuf/encoding/protowire"
)

// RemoteWriteConfig holds configuration for Prometheus remote write.
type RemoteWriteConfig struct {
	URL      string
	Username string
	Password string
	Interval time.Duration
}

// StartRemoteWrite periodically gathers metrics from the registry and pushes
// them to a Prometheus remote write endpoint (e.g. Grafana Cloud).
// It blocks until ctx is cancelled.
func StartRemoteWrite(ctx context.Context, cfg RemoteWriteConfig) {
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	log.Info().
		Str("url", cfg.URL).
		Dur("interval", cfg.Interval).
		Msg("Starting Prometheus remote write")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Stopping Prometheus remote write")
			return
		case <-ticker.C:
			if err := gatherAndPush(ctx, cfg); err != nil {
				log.Error().Err(err).Msg("Failed to push metrics via remote write")
			}
		}
	}
}

func gatherAndPush(ctx context.Context, cfg RemoteWriteConfig) error {
	families, err := Registry.Gather()
	if err != nil {
		return fmt.Errorf("gathering metrics: %w", err)
	}

	data := encodeWriteRequest(families)
	compressed := snappy.Encode(nil, data)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.SetBasicAuth(cfg.Username, cfg.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("remote write returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

type labelPair struct {
	name, value string
}

// encodeWriteRequest converts gathered metric families into Prometheus remote
// write protobuf format (WriteRequest) without requiring the prompb package.
//
// Wire format:
//
//	WriteRequest { repeated TimeSeries timeseries = 1; }
//	TimeSeries   { repeated Label labels = 1; repeated Sample samples = 2; }
//	Label        { string name = 1; string value = 2; }
//	Sample       { double value = 1; int64 timestamp = 2; }
func encodeWriteRequest(families []*dto.MetricFamily) []byte {
	now := time.Now().UnixMilli()
	var req []byte

	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			baseLabels := []labelPair{{name: "__name__", value: fam.GetName()}}
			for _, lp := range m.GetLabel() {
				baseLabels = append(baseLabels, labelPair{name: lp.GetName(), value: lp.GetValue()})
			}
			sort.Slice(baseLabels, func(i, j int) bool {
				return baseLabels[i].name < baseLabels[j].name
			})

			switch fam.GetType() {
			case dto.MetricType_COUNTER:
				req = appendTimeSeries(req, baseLabels, m.GetCounter().GetValue(), now)

			case dto.MetricType_GAUGE:
				req = appendTimeSeries(req, baseLabels, m.GetGauge().GetValue(), now)

			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				req = appendTimeSeries(req, withSuffix(baseLabels, "_count"), float64(h.GetSampleCount()), now)
				req = appendTimeSeries(req, withSuffix(baseLabels, "_sum"), h.GetSampleSum(), now)
				for _, b := range h.GetBucket() {
					bl := withLabel(withSuffix(baseLabels, "_bucket"), "le", formatFloat(b.GetUpperBound()))
					req = appendTimeSeries(req, bl, float64(b.GetCumulativeCount()), now)
				}
				infLabels := withLabel(withSuffix(baseLabels, "_bucket"), "le", "+Inf")
				req = appendTimeSeries(req, infLabels, float64(h.GetSampleCount()), now)

			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				req = appendTimeSeries(req, withSuffix(baseLabels, "_count"), float64(s.GetSampleCount()), now)
				req = appendTimeSeries(req, withSuffix(baseLabels, "_sum"), s.GetSampleSum(), now)
				for _, q := range s.GetQuantile() {
					ql := withLabel(baseLabels, "quantile", formatFloat(q.GetQuantile()))
					req = appendTimeSeries(req, ql, q.GetValue(), now)
				}

			case dto.MetricType_UNTYPED:
				req = appendTimeSeries(req, baseLabels, m.GetUntyped().GetValue(), now)
			}
		}
	}

	return req
}

func appendTimeSeries(req []byte, labels []labelPair, value float64, timestampMs int64) []byte {
	var ts []byte
	for _, l := range labels {
		lbl := encodeLabel(l.name, l.value)
		ts = protowire.AppendTag(ts, 1, protowire.BytesType)
		ts = protowire.AppendBytes(ts, lbl)
	}
	sample := encodeSample(value, timestampMs)
	ts = protowire.AppendTag(ts, 2, protowire.BytesType)
	ts = protowire.AppendBytes(ts, sample)

	req = protowire.AppendTag(req, 1, protowire.BytesType)
	req = protowire.AppendBytes(req, ts)
	return req
}

func encodeLabel(name, value string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, name)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, value)
	return b
}

func encodeSample(value float64, timestampMs int64) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	bits := math.Float64bits(value)
	b = binary.LittleEndian.AppendUint64(b, bits)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(timestampMs))
	return b
}

func withSuffix(labels []labelPair, suffix string) []labelPair {
	out := make([]labelPair, len(labels))
	copy(out, labels)
	for i, l := range out {
		if l.name == "__name__" {
			out[i].value += suffix
			break
		}
	}
	return out
}

func withLabel(labels []labelPair, name, value string) []labelPair {
	out := make([]labelPair, len(labels))
	copy(out, labels)
	out = append(out, labelPair{name: name, value: value})
	sort.Slice(out, func(i, j int) bool {
		return out[i].name < out[j].name
	})
	return out
}

func formatFloat(v float64) string {
	if math.IsInf(v, +1) {
		return "+Inf"
	}
	return fmt.Sprintf("%g", v)
}
