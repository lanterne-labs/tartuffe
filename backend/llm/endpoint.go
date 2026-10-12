package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// Endpoint is an AI endpoint that answers a call instead of the
// OpenAI-compatible server of the Client: the Message Batches API
// (ClaudeBatchEndpoint) or the claude CLI on a Team seat
// (ClaudeTeamEndpoint). opts are the Claude settings of the route of the
// call; Client.Chat gets them from the active routing.
type Endpoint interface {
	Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error)
}

// EndpointName names the endpoint of a route.
type EndpointName string

const (
	// EndpointLocal is the path of Client.Chat: the OpenAI-compatible server
	// (OPEN_AI_API), the local thinking table and the failover to OpenRouter.
	EndpointLocal       EndpointName = "local"
	EndpointClaudeBatch EndpointName = "claude-batch" // ClaudeBatchEndpoint
	EndpointClaudeTeam  EndpointName = "claude-team"  // ClaudeTeamEndpoint
)

// Route says where the calls of one task go. Model and Effort are the Claude
// settings of the call; a local route has neither.
type Route struct {
	Endpoint EndpointName
	Model    string
	Effort   Effort
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
}

// Route returns the route of task.
func (r *Routing) Route(task Task) Route {
	if rt, ok := r.Tasks[task]; ok {
		return rt
	}
	return r.Default
}

// validate reports an endpoint instance of the wrong type or under the local
// name, a route with no endpoint instance, an unknown task, or a Claude route
// without a known model and effort.
func (r *Routing) validate() error {
	for name, e := range r.Endpoints {
		var ok bool
		switch name {
		case EndpointClaudeBatch:
			_, ok = e.(*ClaudeBatchEndpoint)
		case EndpointClaudeTeam:
			_, ok = e.(*ClaudeTeamEndpoint)
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

func (r *Routing) validateRoute(rt Route) error {
	switch rt.Endpoint {
	case EndpointLocal:
		if rt.Model != "" || rt.Effort != "" {
			return errors.New("a local route has no model and no effort")
		}
		return nil
	case EndpointClaudeBatch, EndpointClaudeTeam:
	default:
		return fmt.Errorf("unknown endpoint %q", rt.Endpoint)
	}
	if r.Endpoints[rt.Endpoint] == nil {
		return fmt.Errorf("no instance of endpoint %q", rt.Endpoint)
	}
	if _, ok := claudePriceKey(rt.Model); !ok {
		return fmt.Errorf("unknown claude model %q", rt.Model)
	}
	if _, err := parseEffort(string(rt.Effort)); err != nil {
		return err
	}
	return nil
}

// ApplyOverrides parses spec, "task=model[:effort],task=model[:effort]", and
// returns a copy of r that routes each task of spec to the endpoint of the
// default route with that model. A task without an effort keeps the effort
// rule of ClaudeRouting. r does not change. An empty spec gives a copy of r.
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
		out.Tasks[task] = Route{Endpoint: r.Default.Endpoint, Model: model, Effort: effort}
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
	return fmt.Sprintf("%s %s effort=%s", rt.Endpoint, rt.Model, rt.Effort)
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

// claudeTeamOpusTasks are the grouping chain and the audit. Measured
// (2026-10-10/11): occurrence 1 % wrong groups (Sonnet 5 %), dedup-batch
// 16/17 (Sonnet 12/18 with 4 refusals), entity-dedup 21/21 (Sonnet 20/21),
// audit 24/26 false contradictions removed (Sonnet 20/26).
var claudeTeamOpusTasks = []Task{TaskOccurrence, TaskDedupBatch, TaskEntityDedup, TaskAudit}

// profileRouting builds the routing of a profile, with the endpoints that it
// needs:
//   - local: every task to the local endpoint.
//   - claude-team: every task to one Team seat (CLAUDE_TEAM_PLAN_TOKENS and
//     CLAUDE_TEAM_PLAN_SEAT), with the claude CLI on the PATH.
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
		// Sonnet 5.5 serves every task, except the grouping chain and the
		// audit: there Opus 5.5 made clearly fewer errors on the goldens and
		// evals of 2026-10-10/11 (mirror-rxl.10). The effort rule stays.
		for _, task := range claudeTeamOpusTasks {
			r.Tasks[task] = Route{Endpoint: EndpointClaudeTeam, Model: claudeTeamOpusModel, Effort: claudeEffort(task)}
		}
		return r, nil
	default:
		return nil, fmt.Errorf("unknown LLM_PROFILE %q (known: %s, %s)", profile, profileLocal, profileClaudeTeam)
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
