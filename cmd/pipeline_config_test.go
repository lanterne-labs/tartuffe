package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(s string) *time.Time {
	t, err := time.ParseInLocation(dateLayout, s, time.UTC)
	if err != nil {
		panic(err)
	}
	return &t
}

// TestPipelineConfigZeroValueIsTheNightlyRun locks the contract the whole design
// rests on: the empty config is the daily pipeline, so there is only ever one
// pipeline implementation.
func TestPipelineConfigZeroValueIsTheNightlyRun(t *testing.T) {
	var cfg PipelineConfig

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "daily", cfg.DefaultName())
	assert.False(t, cfg.scoped())
	assert.False(t, cfg.hasDateWindow())
	assert.Equal(t, allPipelineSteps, cfg.enabledSteps())
	for _, step := range allPipelineSteps {
		assert.True(t, cfg.enabled(step), "step %s must run by default", step)
	}
	assert.Equal(t, defaultNewsMaxParse, cfg.maxParse())
	assert.Equal(t, defaultNewsLocation, cfg.location())
}

func TestPipelineConfigStepSelection(t *testing.T) {
	cfg := PipelineConfig{Steps: []string{StepNews, StepContradictions}}

	assert.True(t, cfg.enabled(StepNews))
	assert.True(t, cfg.enabled(StepContradictions))
	assert.False(t, cfg.enabled(StepTwitter))
	assert.False(t, cfg.enabled(StepPhotos))
	// enabledSteps reports execution order, not the order the flags were given.
	assert.Equal(t, []string{StepNews, StepContradictions}, cfg.enabledSteps())
}

func TestResolveSteps(t *testing.T) {
	all, err := resolveSteps(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, all, "no flags means the stored config stays empty = every step")

	only, err := resolveSteps([]string{StepNews}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{StepNews}, only)

	skipped, err := resolveSteps(nil, []string{StepFrpolitics, StepPhotos})
	require.NoError(t, err)
	assert.NotContains(t, skipped, StepFrpolitics)
	assert.NotContains(t, skipped, StepPhotos)
	assert.Contains(t, skipped, StepNews)
	assert.Len(t, skipped, len(allPipelineSteps)-2)

	_, err = resolveSteps([]string{"nope"}, nil)
	assert.Error(t, err, "an unknown step must fail at enqueue, not on the worker")

	_, err = resolveSteps(nil, allPipelineSteps)
	assert.Error(t, err, "skipping everything leaves nothing to run")
}

func TestPipelineConfigValidate(t *testing.T) {
	inverted := PipelineConfig{DateFrom: day("2024-06-30"), DateTo: day("2024-06-01")}
	assert.Error(t, inverted.Validate())

	windowNoStart := PipelineConfig{WindowDays: 7}
	assert.Error(t, windowNoStart.Validate())

	unknownStep := PipelineConfig{Steps: []string{"teleport"}}
	assert.Error(t, unknownStep.Validate())

	ok := PipelineConfig{
		Targets:    []string{"Emmanuel Macron"},
		DateFrom:   day("2024-06-01"),
		DateTo:     day("2024-06-30"),
		WindowDays: 7,
		Steps:      []string{StepNews},
	}
	assert.NoError(t, ok.Validate())
}

// TestPipelineConfigDefaultName covers the naming rule that gives the queue its
// idempotency: same configuration -> same name -> no duplicate pending job.
func TestPipelineConfigDefaultName(t *testing.T) {
	cases := []struct {
		name string
		cfg  PipelineConfig
		want string
	}{
		{"nightly", PipelineConfig{}, "daily"},
		{
			"single day",
			PipelineConfig{Targets: []string{"Emmanuel Macron"}, DateFrom: day("2017-05-07"), DateTo: day("2017-05-07")},
			"emmanuel-macron-2017-05-07",
		},
		{
			"range",
			PipelineConfig{Targets: []string{"Marine Le Pen"}, DateFrom: day("2024-06-01"), DateTo: day("2024-06-30")},
			"marine-le-pen-2024-06-01..2024-06-30",
		},
		{
			"steps subset",
			PipelineConfig{Targets: []string{"Jean-Luc Mélenchon"}, Steps: []string{StepNews, StepContradictions}},
			"jean-luc-m-lenchon-contradictions+news",
		},
		{
			"multiple targets",
			PipelineConfig{Targets: []string{"Macron", "Le Pen"}},
			"macron-le-pen",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.DefaultName())
		})
	}
}

// TestPipelineConfigRoundTrip verifies a queued job replays exactly as
// configured — the worker rebuilds the config from the stored JSONB.
func TestPipelineConfigRoundTrip(t *testing.T) {
	cfg := PipelineConfig{
		Targets:    []string{"Emmanuel Macron"},
		DateFrom:   day("2017-05-01"),
		DateTo:     day("2017-05-31"),
		Steps:      []string{StepNews, StepContradictions},
		MaxParse:   20,
		WindowDays: 7,
		Location:   "France",
	}
	raw, err := cfg.MarshalConfig()
	require.NoError(t, err)

	got, err := parsePipelineConfig(raw)
	require.NoError(t, err)
	assert.Equal(t, cfg.Targets, got.Targets)
	assert.Equal(t, cfg.Steps, got.Steps)
	assert.Equal(t, cfg.MaxParse, got.MaxParse)
	assert.Equal(t, cfg.WindowDays, got.WindowDays)
	assert.Equal(t, cfg.Location, got.Location)
	require.NotNil(t, got.DateFrom)
	require.NotNil(t, got.DateTo)
	assert.True(t, cfg.DateFrom.Equal(*got.DateFrom))
	assert.True(t, cfg.DateTo.Equal(*got.DateTo))

	// An empty blob is the nightly default, not a parse error.
	empty, err := parsePipelineConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, "daily", empty.DefaultName())

	// A config that would fail at run time is rejected at load time.
	_, err = parsePipelineConfig([]byte(`{"steps":["teleport"]}`))
	assert.Error(t, err)
}

// TestParseWindowDate verifies --to covers the whole day, so a single-date job
// doesn't silently exclude everything published after midnight.
func TestParseWindowDate(t *testing.T) {
	none, err := parseWindowDate("", false)
	require.NoError(t, err)
	assert.Nil(t, none)

	from, err := parseWindowDate("2017-05-07", false)
	require.NoError(t, err)
	assert.Equal(t, "2017-05-07T00:00:00Z", from.Format(time.RFC3339))

	to, err := parseWindowDate("2017-05-07", true)
	require.NoError(t, err)
	assert.Equal(t, "2017-05-07T23:59:59Z", to.Format(time.RFC3339))

	_, err = parseWindowDate("07/05/2017", false)
	assert.Error(t, err)
}
