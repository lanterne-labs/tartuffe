package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Endpoint is an AI endpoint that answers a call instead of the
// OpenAI-compatible server of the Client: the Message Batches API
// (ClaudeBatchEndpoint), the claude CLI on the Team seats (ClaudeTeamEndpoint)
// or the sync Messages API (ClaudeAPIEndpoint). opts are the Claude settings
// of the route of the call; Client.Chat gets them from the active routing.
// UsageReport gives the tokens and the cost per task and model.
type Endpoint interface {
	Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error)
	UsageReport() string
}

// EndpointName names the endpoint of a route.
type EndpointName string

const (
	// EndpointLocal is the path of Client.Chat: the OpenAI-compatible server
	// (OPEN_AI_API), the local thinking table and the failover to OpenRouter.
	EndpointLocal       EndpointName = "local"
	EndpointClaudeBatch EndpointName = "claude-batch" // ClaudeBatchEndpoint
	EndpointClaudeTeam  EndpointName = "claude-team"  // ClaudeTeamEndpoint
	EndpointClaudeAPI   EndpointName = "claude-api"   // ClaudeAPIEndpoint
)

// Route says where the calls of one task go. Model and Effort are the Claude
// settings of the call; a local route has neither. Fallback is "" or a Claude
// endpoint that gets the call, with the same model and effort, when Endpoint
// returns an UnavailableError (Client.Chat).
type Route struct {
	Endpoint EndpointName
	Model    string
	Effort   Effort
	Fallback EndpointName
}

// claudeOptions returns the settings that an endpoint gets for a call on
// route rt.
func (rt Route) claudeOptions() ClaudeOptions {
	return ClaudeOptions{Model: rt.Model, Effort: rt.Effort, ThinkingTokens: claudeThinkingTokens}
}

// Routing maps each task to a route. Default is the route of a task with no
// entry in Tasks, and of a call without a task. Endpoints holds the instance
// of each endpoint that a route names, except the local one.
type Routing struct {
	Default   Route
	Tasks     map[Task]Route
	Endpoints map[EndpointName]Endpoint

	switched sync.Map // fallbackSwitch -> true: the switches already logged
	skips    endpointSkips
}

// fallbackSwitch names a switch of the calls of a task from an endpoint to
// its fallback.
type fallbackSwitch struct {
	task     Task
	from, to EndpointName
}

// call sends req to the endpoint of the Claude route rt. When that endpoint
// returns an UnavailableError and rt has a fallback, call sends req once to
// the fallback and returns its result. Until the endpoint can serve again
// (endpointSkips), the calls of the routes with a fallback go to the
// fallback at once. A route without a fallback always calls its endpoint, so
// the gate gets the error and waits as before. The first switch of each task
// logs a warning; the next ones do not, so a seat at its limit does not flood
// the log. Any other error of the endpoint is returned as is.
func (r *Routing) call(ctx context.Context, req ChatRequest, rt Route) (string, *ChatResponse, error) {
	primary := r.Endpoints[rt.Endpoint]
	if rt.Fallback == "" {
		return primary.Call(ctx, req, rt.claudeOptions())
	}
	try, tok, err := r.skips.try(rt.Endpoint)
	if try {
		if tok.probe {
			defer r.skips.release(rt.Endpoint, tok)
		}
		var content string
		var resp *ChatResponse
		content, resp, err = primary.Call(ctx, req, rt.claudeOptions())
		r.skips.record(ctx, rt.Endpoint, tok, err)
		var ue *UnavailableError
		if !errors.As(err, &ue) {
			return content, resp, err
		}
	}
	if _, seen := r.switched.LoadOrStore(fallbackSwitch{req.Task, rt.Endpoint, rt.Fallback}, true); !seen {
		log.Warn().Err(err).Str("task", string(req.Task)).Str("from", string(rt.Endpoint)).
			Str("to", string(rt.Fallback)).Msg("LLM endpoint unavailable: the calls of the task go to the fallback")
	}
	content, resp, ferr := r.Endpoints[rt.Fallback].Call(ctx, req, rt.claudeOptions())
	if ferr != nil {
		return "", resp, bothFailed(rt, err, ferr)
	}
	return content, resp, nil
}

// bothFailed returns the error of a call that failed on the endpoint of rt
// with err, an UnavailableError, and then on the fallback with ferr. When
// both are UnavailableErrors, it returns the one with the earliest non-zero
// RetryAt, so the gate waits for the first endpoint that can serve again: a
// seat that resets in 20 min with the API at its cap gives the RetryAt of
// the seat. The message always holds both errors.
func bothFailed(rt Route, err, ferr error) error {
	both := fmt.Errorf("fallback %s: %w (after %s: %v)", rt.Fallback, ferr, rt.Endpoint, err)
	var pue, fue *UnavailableError
	if !errors.As(ferr, &fue) || !errors.As(err, &pue) || pue.RetryAt.IsZero() {
		return both
	}
	if !fue.RetryAt.IsZero() && !fue.RetryAt.After(pue.RetryAt) {
		return both
	}
	return &UnavailableError{
		Endpoint: pue.Endpoint,
		RetryAt:  pue.RetryAt,
		Err:      fmt.Errorf("%w (fallback %s: %v)", pue.Err, rt.Fallback, ferr),
	}
}

// unknownResetSkip is how long the routes with a fallback skip an endpoint
// that is unavailable and gave no reset time (a zero RetryAt).
const unknownResetSkip = 15 * time.Minute

// endpointSkips holds the endpoints that are unavailable until a known time.
// The routes with a fallback skip such an endpoint. After that time, one call
// (the probe) tries the endpoint again, and the other calls still go to the
// fallback until the probe ends. The state is per endpoint, not per task: a
// seat at its limit is at its limit for every task.
type endpointSkips struct {
	mu     sync.Mutex
	lastID uint64
	m      map[EndpointName]*endpointSkip
}

type endpointSkip struct {
	id      uint64
	until   time.Time
	probing bool  // the probe of this skip is in flight
	cause   error // the UnavailableError that started or renewed the skip
}

// skipToken names a call that try let through to the endpoint. probe is
// true for the probe of the skip id. Only the probe can change or end an
// existing skip: a call that started before the skip cannot end it.
type skipToken struct {
	id    uint64
	probe bool
}

// try reports whether a call goes to endpoint name. When it does, tok names
// the call for record and release. When it does not, cause is the error that
// made the endpoint unavailable.
func (s *endpointSkips) try(name EndpointName) (ok bool, tok skipToken, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk := s.m[name]
	if sk == nil {
		return true, skipToken{}, nil
	}
	if sk.probing || time.Now().Before(sk.until) {
		return false, skipToken{}, sk.cause
	}
	sk.probing = true
	return true, skipToken{id: sk.id, probe: true}, nil
}

// record updates the state of endpoint name with err, the result of the
// call tok to it:
//   - With a skip, only its probe changes the state.
//   - An UnavailableError starts or renews the skip until its RetryAt, or for
//     unknownResetSkip when RetryAt is zero.
//   - A cancelled call says nothing about the endpoint.
//   - Any other result means that the endpoint serves: the skip ends.
func (s *endpointSkips) record(ctx context.Context, name EndpointName, tok skipToken, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk := s.m[name]
	if sk != nil && (!tok.probe || tok.id != sk.id) {
		return
	}
	var ue *UnavailableError
	switch {
	case errors.As(err, &ue):
		until := ue.RetryAt
		if until.IsZero() {
			until = time.Now().Add(unknownResetSkip)
		}
		if sk == nil {
			if s.m == nil {
				s.m = map[EndpointName]*endpointSkip{}
			}
			s.lastID++
			sk = &endpointSkip{id: s.lastID}
			s.m[name] = sk
			log.Warn().Err(err).Str("endpoint", string(name)).Time("until", until).
				Msg("LLM endpoint unavailable: the routes with a fallback skip it until it can serve again")
		}
		sk.until, sk.cause = until, err
	case sk == nil:
	case err != nil && ctx.Err() != nil:
	default:
		delete(s.m, name)
		log.Info().Str("endpoint", string(name)).Msg("LLM endpoint serves again: the routes with a fallback use it again")
	}
}

// release frees the probe slot of tok, when its skip still exists.
func (s *endpointSkips) release(name EndpointName, tok skipToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sk := s.m[name]; sk != nil && sk.id == tok.id {
		sk.probing = false
	}
}

// Route returns the route of task.
func (r *Routing) Route(task Task) Route {
	if rt, ok := r.Tasks[task]; ok {
		return rt
	}
	return r.Default
}

// validate reports an endpoint instance of the wrong type or under the local
// name, a route with no endpoint instance, an unknown task, a Claude route
// without a known model and effort, or a bad fallback.
func (r *Routing) validate() error {
	for name, e := range r.Endpoints {
		var ok bool
		switch name {
		case EndpointClaudeBatch:
			_, ok = e.(*ClaudeBatchEndpoint)
		case EndpointClaudeTeam:
			_, ok = e.(*ClaudeTeamEndpoint)
		case EndpointClaudeAPI:
			_, ok = e.(*ClaudeAPIEndpoint)
		default:
			return fmt.Errorf("endpoint %q takes no instance", name)
		}
		if !ok {
			return fmt.Errorf("the instance of endpoint %q is a %T", name, e)
		}
	}
	if err := r.validateRoute(r.Default); err != nil {
		return fmt.Errorf("default route: %w", err)
	}
	known := knownTasks()
	for task, rt := range r.Tasks {
		if !known[task] {
			return fmt.Errorf("route of unknown task %q", task)
		}
		if err := r.validateRoute(rt); err != nil {
			return fmt.Errorf("route of task %q: %w", task, err)
		}
	}
	return nil
}

// validateRoute checks rt. A local route has no fallback: the local endpoint
// has its own failover to OpenRouter. A Claude route has no local fallback:
// a prompt sized for Claude can be too large for the context of the local
// model.
func (r *Routing) validateRoute(rt Route) error {
	if rt.Endpoint == EndpointLocal {
		if rt.Model != "" || rt.Effort != "" || rt.Fallback != "" {
			return errors.New("a local route has no model, no effort and no fallback")
		}
		return nil
	}
	if err := r.validateClaudeEndpoint(rt.Endpoint); err != nil {
		return err
	}
	if rt.Fallback != "" {
		if rt.Fallback == rt.Endpoint {
			return fmt.Errorf("fallback %q is the endpoint of the route", rt.Fallback)
		}
		if err := r.validateClaudeEndpoint(rt.Fallback); err != nil {
			return fmt.Errorf("fallback: %w", err)
		}
	}
	if _, ok := claudePriceKey(rt.Model); !ok {
		return fmt.Errorf("unknown claude model %q", rt.Model)
	}
	if _, err := parseEffort(string(rt.Effort)); err != nil {
		return err
	}
	return nil
}

// validateClaudeEndpoint reports a name that is not a Claude endpoint, or a
// Claude endpoint with no instance.
func (r *Routing) validateClaudeEndpoint(name EndpointName) error {
	switch name {
	case EndpointClaudeBatch, EndpointClaudeTeam, EndpointClaudeAPI:
	default:
		return fmt.Errorf("unknown claude endpoint %q", name)
	}
	if r.Endpoints[name] == nil {
		return fmt.Errorf("no instance of endpoint %q", name)
	}
	return nil
}

// ApplyOverrides parses spec, "task=model[:effort],task=model[:effort]", and
// returns a copy of r that routes each task of spec to the endpoint and the
// fallback of the default route with that model. A task without an effort
// keeps the effort rule of ClaudeRouting. r does not change. An empty spec
// gives a copy of r.
func (r *Routing) ApplyOverrides(spec string) (*Routing, error) {
	out := &Routing{
		Default:   r.Default,
		Tasks:     make(map[Task]Route, len(r.Tasks)),
		Endpoints: make(map[EndpointName]Endpoint, len(r.Endpoints)),
	}
	for task, rt := range r.Tasks {
		out.Tasks[task] = rt
	}
	for name, e := range r.Endpoints {
		out.Endpoints[name] = e
	}
	if strings.TrimSpace(spec) == "" {
		return out, nil
	}
	if r.Default.Endpoint == EndpointLocal {
		return nil, errors.New("per-task models need a Claude endpoint as the default route")
	}
	known := knownTasks()
	seen := map[Task]bool{}
	for _, entry := range strings.Split(spec, ",") {
		name, setting, ok := strings.Cut(strings.TrimSpace(entry), "=")
		task := Task(strings.TrimSpace(name))
		if !ok || !known[task] {
			return nil, fmt.Errorf("entry %q: want task=model[:effort] with a task of %v", entry, AllTasks)
		}
		if seen[task] {
			return nil, fmt.Errorf("entry %q: task %q is in the list 2 times", entry, task)
		}
		seen[task] = true
		model, effortText, hasEffort := strings.Cut(strings.TrimSpace(setting), ":")
		if _, ok := claudePriceKey(model); !ok {
			return nil, fmt.Errorf("entry %q: unknown claude model %q", entry, model)
		}
		effort := claudeEffort(task)
		if hasEffort {
			var err error
			if effort, err = parseEffort(effortText); err != nil {
				return nil, fmt.Errorf("entry %q: %w", entry, err)
			}
		}
		out.Tasks[task] = Route{Endpoint: r.Default.Endpoint, Model: model, Effort: effort, Fallback: r.Default.Fallback}
	}
	return out, nil
}

// String returns the default route and one line per task route, in task
// order.
func (r *Routing) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-15s %s\n", "(default)", r.Default)
	tasks := make([]string, 0, len(r.Tasks))
	for task := range r.Tasks {
		tasks = append(tasks, string(task))
	}
	sort.Strings(tasks)
	for _, task := range tasks {
		fmt.Fprintf(&b, "%-15s %s\n", task, r.Tasks[Task(task)])
	}
	return b.String()
}

func (rt Route) String() string {
	if rt.Endpoint == EndpointLocal {
		return string(rt.Endpoint)
	}
	s := fmt.Sprintf("%s %s effort=%s", rt.Endpoint, rt.Model, rt.Effort)
	if rt.Fallback != "" {
		s += " fallback=" + string(rt.Fallback)
	}
	return s
}

// ClaudeRouting routes every task, and a call without a task, to the Claude
// endpoint e under name, with model. The effort follows the local thinking
// table (claudeEffort).
func ClaudeRouting(name EndpointName, e Endpoint, model string) *Routing {
	r := &Routing{
		Default:   Route{Endpoint: name, Model: model, Effort: claudeEffort("")},
		Tasks:     make(map[Task]Route, len(AllTasks)),
		Endpoints: map[EndpointName]Endpoint{name: e},
	}
	for _, task := range AllTasks {
		r.Tasks[task] = Route{Endpoint: name, Model: model, Effort: claudeEffort(task)}
	}
	return r
}

// claudeEffort gives effort medium to a task that thinks on the local model,
// and effort low to the others. Opus 5.5 and Sonnet 5.5 cannot turn the
// thinking off, so low is the plain setting.
func claudeEffort(task Task) Effort {
	if task != "" && Thinks(task) {
		return EffortMedium
	}
	return EffortLow
}

func parseEffort(s string) (Effort, error) {
	switch e := Effort(strings.TrimSpace(s)); e {
	case EffortLow, EffortMedium, EffortHigh:
		return e, nil
	default:
		return "", fmt.Errorf("unknown effort %q (known: %s, %s, %s)", s, EffortLow, EffortMedium, EffortHigh)
	}
}

// claudeThinkingTokens is the room for the thinking on top of the answer
// budget of a Claude call. Opus 5.5 and Sonnet 5.5 always think a little,
// even at effort low; a reply that stops at max_tokens fails. The usage
// reports show the thinking tokens to set this from.
const claudeThinkingTokens = 8000

var (
	routingMu sync.RWMutex
	routing   = localRouting()
)

// concurrentEndpoint is an endpoint that serves more than one call at a
// time.
type concurrentEndpoint interface {
	Concurrency() int
}

// Concurrency is the number of calls the default route of the active routing
// serves at one time: what a caller may send at once. The local endpoint
// serves one (the GPU box runs one request), as does any endpoint that does
// not say otherwise.
func Concurrency() int {
	r := activeRouting()
	if r == nil {
		return 1
	}
	if e, ok := r.Endpoints[r.Default.Endpoint].(concurrentEndpoint); ok {
		return max(1, e.Concurrency())
	}
	return 1
}

// seatedEndpoint is an endpoint served by seats that reach usage limits.
type seatedEndpoint interface {
	Serving() bool
}

// SeatsServing reports whether the default route's seats can serve now: false
// only when every seat is at its limit, so the calls go to the API key.
// Without seats (the local box) it is true.
func SeatsServing() bool {
	r := activeRouting()
	if r == nil {
		return true
	}
	if e, ok := r.Endpoints[r.Default.Endpoint].(seatedEndpoint); ok {
		return e.Serving()
	}
	return true
}

// steadyEndpoint is an endpoint whose answers need no vote.
type steadyEndpoint interface {
	Steady() bool
}

// Votes is how many samples a judge of task takes on the active routing: its
// votes on the local GPU box, whose verdicts flip between runs (a smaller
// model, and a verdict there depends on what else is in flight), and 1 on a
// Claude endpoint, where a sample is a call of the seats' budget that the
// vote does not earn back. A contradiction still goes through the verifier.
func Votes(task Task, votes int) int {
	if Steady(task) {
		return 1
	}
	return votes
}

// Steady reports whether the endpoint of task on the active routing gives
// the same verdict to the same prompt from one run to the next (a Claude
// endpoint): a judge there asks once, and a prompt there can carry several
// pairs without the verdicts of one leaking into another. False without a
// routing, and on the local GPU box.
func Steady(task Task) bool {
	r := activeRouting()
	if r == nil {
		return false
	}
	e, ok := r.Endpoints[r.Route(task).Endpoint].(steadyEndpoint)
	return ok && e.Steady()
}

// LocalContextTokens is the context of a call on the local endpoint: the
// mirror-llm slot holds 16k tokens (llama-swap entry: -c 32768 -np 2), 14k of
// them usable. An endpoint that does not say otherwise gets it.
const LocalContextTokens = 14000

// claudeContextTokens is the context of a Claude call (Sonnet and Opus 5.5).
const claudeContextTokens = 200000

// contextEndpoint is an endpoint that says how many tokens one call may hold.
type contextEndpoint interface {
	ContextTokens() int
}

// ContextTokens is the number of tokens (prompt and answer) a call of task
// may hold on the endpoint of its route in the active routing. A caller that
// splits its input (the extraction of a long page) sizes its pieces from it:
// sized for the local box, a live blog went to Claude in 113 pieces of 1,500
// characters, and quotes lost the name of their speaker.
func ContextTokens(task Task) int {
	r := activeRouting()
	if r == nil {
		return LocalContextTokens
	}
	if e, ok := r.Endpoints[r.Route(task).Endpoint].(contextEndpoint); ok {
		return e.ContextTokens()
	}
	return LocalContextTokens
}

func activeRouting() *Routing {
	routingMu.RLock()
	defer routingMu.RUnlock()
	return routing
}

// SetRouting makes r the routing of every Client.Chat call of the process
// until restore is called. The process start installs the routing of
// LLM_PROFILE (InstallProfileFromEnv); the goldens install their own.
func SetRouting(r *Routing) (restore func(), err error) {
	if err := r.validate(); err != nil {
		return nil, fmt.Errorf("LLM routing: %w", err)
	}
	routingMu.Lock()
	prev := routing
	routing = r
	routingMu.Unlock()
	return func() {
		routingMu.Lock()
		routing = prev
		routingMu.Unlock()
	}, nil
}

// The profiles of LLM_PROFILE.
const (
	profileLocal      = "local"
	profileClaudeTeam = "claude-team"
)

// claudeTeamModel is the model of the claude-team profile, except for
// claudeTeamOpusTasks.
const claudeTeamModel = "claude-sonnet-5-5"

// claudeTeamOpusModel serves claudeTeamOpusTasks on the claude-team profile.
const claudeTeamOpusModel = "claude-opus-5-5"

// claudeTeamOpusTasks are the grouping chain, the audit and the verifier.
// Measured (2026-10-10/11): occurrence 1 % wrong groups (Sonnet 5 %),
// dedup-batch 16/17 (Sonnet 12/18 with 4 refusals), entity-dedup 21/21
// (Sonnet 20/21), audit 24/26 false contradictions removed (Sonnet 20/26).
// Verify (2026-10-28, 191 reviewed production contradictions, mirror-1g9):
// false ones dropped 85/109 (Sonnet 68/109), real ones kept 58/82 (Sonnet
// 62/82); Opus applies the rules no prompt states yet (44/46 dropped against
// 35/45). One call per proposed contradiction: a small share of the calls.
var claudeTeamOpusTasks = []Task{TaskOccurrence, TaskDedupBatch, TaskEntityDedup, TaskAudit, TaskVerify}

// profileRouting builds the routing of a profile, with the endpoints that it
// needs:
//   - local: every task to the local endpoint.
//   - claude-team: every task to the Team seats (CLAUDE_TEAM_PLAN_TOKENS; a
//     seat at its usage limit gives way to the next), with the claude CLI on
//     the PATH. When
//     ANTHROPIC_API_KEY is set, every route has the sync API as its
//     fallback, with the same model and effort: when every seat is at its
//     usage limit, the call overflows to the API, up to claudeAPIRunCapUSD
//     per run.
func profileRouting(profile string) (*Routing, error) {
	switch profile {
	case profileLocal:
		return localRouting(), nil
	case profileClaudeTeam:
		e, err := NewClaudeTeamEndpointFromEnv()
		if err != nil {
			return nil, fmt.Errorf("LLM_PROFILE %s: %w", profile, err)
		}
		r := ClaudeRouting(EndpointClaudeTeam, e, claudeTeamModel)
		// Sonnet 5.5 serves every task, except the grouping chain, the audit
		// and the verifier: there Opus 5.5 made clearly fewer errors on the
		// goldens and evals (see claudeTeamOpusTasks). The effort rule stays.
		for _, task := range claudeTeamOpusTasks {
			r.Tasks[task] = Route{Endpoint: EndpointClaudeTeam, Model: claudeTeamOpusModel, Effort: claudeEffort(task)}
		}
		if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
			log.Info().Msg("LLM_PROFILE claude-team: ANTHROPIC_API_KEY is not set; the seat has no overflow to the API")
			return r, nil
		}
		api, err := NewClaudeAPIEndpointFromEnv()
		if err != nil {
			return nil, fmt.Errorf("LLM_PROFILE %s: %w", profile, err)
		}
		r.Endpoints[EndpointClaudeAPI] = api
		r.Default.Fallback = EndpointClaudeAPI
		for task, rt := range r.Tasks {
			rt.Fallback = EndpointClaudeAPI
			r.Tasks[task] = rt
		}
		return r, nil
	default:
		return nil, fmt.Errorf("unknown LLM_PROFILE %q (known: %s, %s)", profile, profileLocal, profileClaudeTeam)
	}
}

// LogUsage logs the usage report of each endpoint of the active routing:
// tokens and cost per task and model, since the start of the process.
func LogUsage() {
	r := activeRouting()
	for name, e := range r.Endpoints {
		log.Info().Str("endpoint", string(name)).Str("usage", e.UsageReport()).Msg("LLM usage of the process")
	}
}

func localRouting() *Routing {
	return &Routing{Default: Route{Endpoint: EndpointLocal}}
}

// InstallProfileFromEnv installs the routing of the profile that LLM_PROFILE
// names; "local" when it is not set. A bad profile fails here, before the
// process does any work. It returns the profile name.
func InstallProfileFromEnv() (string, error) {
	profile := strings.TrimSpace(os.Getenv("LLM_PROFILE"))
	if profile == "" {
		profile = profileLocal
	}
	r, err := profileRouting(profile)
	if err != nil {
		return "", err
	}
	if _, err := SetRouting(r); err != nil {
		return "", fmt.Errorf("LLM_PROFILE %s: %w", profile, err)
	}
	return profile, nil
}
