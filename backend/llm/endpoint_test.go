package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setRouting(t *testing.T, r *Routing) {
	t.Helper()
	restore, err := SetRouting(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}

func TestRoutingRoute(t *testing.T) {
	r := ClaudeRouting(EndpointClaudeTeam, &ClaudeTeamEndpoint{}, "claude-sonnet-5-5")
	if got := r.Route(TaskDetect); got != (Route{Endpoint: EndpointClaudeTeam, Model: "claude-sonnet-5-5", Effort: EffortMedium}) {
		t.Fatalf("detect thinks locally: want effort medium, got %v", got)
	}
	if got := r.Route(TaskAudit); got.Effort != EffortLow {
		t.Fatalf("audit does not think locally: want effort low, got %v", got)
	}
	delete(r.Tasks, TaskAudit)
	if got := r.Route(TaskAudit); got != r.Default {
		t.Fatalf("a task without an entry takes the default route, got %v", got)
	}
	if got := r.Route(""); got != (Route{Endpoint: EndpointClaudeTeam, Model: "claude-sonnet-5-5", Effort: EffortLow}) {
		t.Fatalf("a call without a task takes the default route, got %v", got)
	}
}

func TestRoutingApplyOverrides(t *testing.T) {
	base := ClaudeRouting(EndpointClaudeBatch, &ClaudeBatchEndpoint{}, "claude-haiku-5-5")
	r, err := base.ApplyOverrides("audit=claude-opus-5-5:high, detect=claude-sonnet-5-5:low, verify=claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	for task, want := range map[Task]Route{
		TaskAudit:  {Endpoint: EndpointClaudeBatch, Model: "claude-opus-5-5", Effort: EffortHigh},
		TaskDetect: {Endpoint: EndpointClaudeBatch, Model: "claude-sonnet-5-5", Effort: EffortLow},
		TaskVerify: {Endpoint: EndpointClaudeBatch, Model: "claude-opus-5-5", Effort: EffortLow},  // no effort: the effort rule
		TaskBundle: {Endpoint: EndpointClaudeBatch, Model: "claude-haiku-5-5", Effort: EffortLow}, // no override
	} {
		if got := r.Route(task); got != want {
			t.Errorf("%s: got %v, want %v", task, got, want)
		}
	}
	if got := base.Route(TaskAudit); got.Model != "claude-haiku-5-5" {
		t.Fatalf("ApplyOverrides must not change its receiver, got %v", got)
	}
	if r, err := base.ApplyOverrides(""); err != nil || r == base || r.Route(TaskAudit) != base.Route(TaskAudit) {
		t.Fatalf("an empty spec gives a copy: %v", err)
	}
	for _, bad := range []string{
		"typo=claude-opus-5-5", "audit=gpt-4", "audit=claude-opus-5-5:max", "audit",
		"audit=claude-opus-5-5,", "audit=claude-opus-5-5,audit=claude-haiku-5-5",
	} {
		if _, err := base.ApplyOverrides(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if _, err := localRouting().ApplyOverrides("audit=claude-opus-5-5"); err == nil {
		t.Error("an override on a local routing must fail")
	}
}

func TestSetRoutingValidates(t *testing.T) {
	team := Route{Endpoint: EndpointClaudeTeam, Model: "claude-opus-5-5", Effort: EffortLow}
	bad := map[string]*Routing{
		"unknown endpoint":     {Default: Route{Endpoint: "gpu2"}},
		"local with a model":   {Default: Route{Endpoint: EndpointLocal, Model: "claude-opus-5-5"}},
		"no instance":          {Default: team},
		"unknown task":         {Default: Route{Endpoint: EndpointLocal}, Tasks: map[Task]Route{"typo": {Endpoint: EndpointLocal}}},
		"unknown effort":       {Default: Route{Endpoint: EndpointClaudeTeam, Model: "claude-opus-5-5", Effort: "max"}, Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}}},
		"local instance":       {Default: Route{Endpoint: EndpointLocal}, Endpoints: map[EndpointName]Endpoint{EndpointLocal: &ClaudeTeamEndpoint{}}},
		"instance wrong type":  {Default: team, Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeBatchEndpoint{}}},
		"api wrong type":       {Default: team, Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}, EndpointClaudeAPI: &ClaudeTeamEndpoint{}}},
		"local fallback":       {Default: withFallback(team, EndpointLocal), Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}}},
		"unknown fallback":     {Default: withFallback(team, "gpu2"), Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}}},
		"fallback no instance": {Default: withFallback(team, EndpointClaudeAPI), Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}}},
		"fallback to itself":   {Default: withFallback(team, EndpointClaudeTeam), Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}}},
		"local route with a fallback": {
			Default:   Route{Endpoint: EndpointLocal, Fallback: EndpointClaudeAPI},
			Endpoints: map[EndpointName]Endpoint{EndpointClaudeAPI: &ClaudeAPIEndpoint{}},
		},
		"task route with a bad fallback": {
			Default:   team,
			Tasks:     map[Task]Route{TaskAudit: withFallback(team, EndpointClaudeBatch)},
			Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}},
		},
	}
	good := &Routing{
		Default:   withFallback(team, EndpointClaudeAPI),
		Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: &ClaudeTeamEndpoint{}, EndpointClaudeAPI: &ClaudeAPIEndpoint{}},
	}
	if err := good.validate(); err != nil {
		t.Fatalf("a seat with the API as fallback is valid: %v", err)
	}
	for name, r := range bad {
		if _, err := SetRouting(r); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if got := activeRouting().Route(TaskAudit); got.Endpoint != EndpointLocal {
		t.Fatalf("a rejected routing must not be installed, got %v", got)
	}
}

func withFallback(rt Route, fallback EndpointName) Route {
	rt.Fallback = fallback
	return rt
}

func TestProfileRouting(t *testing.T) {
	r, err := profileRouting(profileLocal)
	if err != nil || r.Route(TaskDetect) != (Route{Endpoint: EndpointLocal}) || r.Route("") != (Route{Endpoint: EndpointLocal}) {
		t.Fatalf("local profile: %v %v", r, err)
	}
	if _, err := profileRouting("claude"); err == nil {
		t.Fatal("an unknown profile must fail")
	}

	_, dir := fakeClaude(t, teamSuccess, "", 0)
	t.Setenv("PATH", dir)
	t.Setenv("CLAUDE_TEAM_PLAN_TOKENS", "alice=tok-alice")
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "alice")
	opus := map[Task]bool{TaskOccurrence: true, TaskDedupBatch: true, TaskEntityDedup: true, TaskAudit: true, TaskVerify: true}
	for _, key := range []string{"", "sk-test"} {
		t.Setenv("ANTHROPIC_API_KEY", key)
		r, err = profileRouting(profileClaudeTeam)
		if err != nil {
			t.Fatal(err)
		}
		// With an API key, every route overflows to the API with the same
		// model and effort; without one, no route has a fallback.
		fallback := EndpointName("")
		if key != "" {
			fallback = EndpointClaudeAPI
		}
		for _, task := range AllTasks {
			model := "claude-sonnet-5-5"
			if opus[task] {
				model = "claude-opus-5-5"
			}
			want := Route{Endpoint: EndpointClaudeTeam, Model: model, Effort: claudeEffort(task), Fallback: fallback}
			if got := r.Route(task); got != want {
				t.Errorf("claude-team profile (key %q), task %s: got %v, want %v", key, task, got, want)
			}
		}
		if got := r.Route("").Fallback; got != fallback {
			t.Errorf("claude-team profile (key %q): the default route has fallback %q", key, got)
		}
		if _, ok := r.Endpoints[EndpointClaudeAPI].(*ClaudeAPIEndpoint); ok != (key != "") {
			t.Errorf("claude-team profile (key %q): API instance present = %v", key, ok)
		}
		if err := r.validate(); err != nil {
			t.Fatal(err)
		}
		over, err := r.ApplyOverrides("detect=claude-opus-5-5")
		if err != nil {
			t.Fatal(err)
		}
		if got := over.Route(TaskDetect); got.Model != "claude-opus-5-5" || got.Fallback != fallback {
			t.Errorf("an override keeps the fallback of the profile (key %q), got %v", key, got)
		}
	}
	t.Setenv("ANTHROPIC_API_KEY", "")

	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "")
	if _, err := profileRouting(profileClaudeTeam); err != nil {
		t.Fatalf("claude-team without CLAUDE_TEAM_PLAN_SEAT must use the seats in the order of the list: %v", err)
	}
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "bob")
	if _, err := profileRouting(profileClaudeTeam); err == nil {
		t.Fatal("claude-team with a first seat that has no token must fail")
	}
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "alice")
	t.Setenv("PATH", t.TempDir())
	if _, err := profileRouting(profileClaudeTeam); err == nil {
		t.Fatal("claude-team without the claude CLI on the PATH must fail")
	}
}

func TestInstallProfileFromEnv(t *testing.T) {
	prev := activeRouting()
	t.Cleanup(func() {
		routingMu.Lock()
		routing = prev
		routingMu.Unlock()
	})
	t.Setenv("LLM_PROFILE", "")
	if p, err := InstallProfileFromEnv(); err != nil || p != profileLocal {
		t.Fatalf("unset LLM_PROFILE: %q %v", p, err)
	}
	t.Setenv("LLM_PROFILE", "nope")
	if _, err := InstallProfileFromEnv(); err == nil {
		t.Fatal("an unknown LLM_PROFILE must fail")
	}
	t.Setenv("LLM_PROFILE", profileClaudeTeam)
	t.Setenv("CLAUDE_TEAM_PLAN_TOKENS", "")
	if _, err := InstallProfileFromEnv(); err == nil {
		t.Fatal("claude-team without CLAUDE_TEAM_PLAN_TOKENS must fail")
	}
}

// The local profile sends the payloads that main sent before the routing
// existed (captured on main 637c9a8): the same bytes for a task that thinks,
// with and without JSON mode, a plain task and a call without a task.
func TestLocalProfileKeepsThePayload(t *testing.T) {
	const ok = `{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`
	const msgs = `"model":"cfg-model","messages":[{"role":"system","content":"rules"},{"role":"user","content":"facts"}],"temperature":0.1`
	const thinking = `"chat_template_kwargs":{"enable_thinking":true},"thinking_budget_tokens":3000`
	cases := []struct {
		task Task
		json bool
		want string
	}{
		{TaskOccurrence, true, `{` + msgs + `,"max_tokens":3300,"response_format":{"type":"json_object"},` + thinking + `}`},
		{TaskDetect, false, `{` + msgs + `,"max_tokens":3300,` + thinking + `}`},
		{TaskAudit, false, `{` + msgs + `,"max_tokens":300}`},
		{"", false, `{` + msgs + `,"max_tokens":300}`},
	}
	r, err := profileRouting(profileLocal)
	if err != nil {
		t.Fatal(err)
	}
	setRouting(t, r)
	for _, c := range cases {
		cl, got := chatTestClient(t, 200, ok)
		req := ChatRequest{
			Messages:    []ChatMessage{{Role: "system", Content: "rules"}, {Role: "user", Content: "facts"}},
			Temperature: 0.1,
			MaxTokens:   300,
			Task:        c.task,
		}
		if c.json {
			req.ResponseFormat = &ResponseFormat{Type: "json_object"}
		}
		if _, _, err := cl.Chat(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if string(*got) != c.want {
			t.Errorf("task %q: payload changed\ngot:  %s\nwant: %s", c.task, *got, c.want)
		}
	}
}

// A task routed to an endpoint goes there, with the model and the effort of
// its route, and not to the server; the other tasks still go to the server.
func TestChatRoutesByTask(t *testing.T) {
	const ok = `{"choices":[{"message":{"role":"assistant","content":"server"},"finish_reason":"stop"}]}`
	c, got := chatTestClient(t, 200, ok)
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	setRouting(t, &Routing{
		Default:   Route{Endpoint: EndpointLocal},
		Tasks:     map[Task]Route{TaskAudit: {Endpoint: EndpointClaudeTeam, Model: "claude-opus-5-5", Effort: EffortHigh}},
		Endpoints: map[EndpointName]Endpoint{EndpointClaudeTeam: teamEndpoint(bin)},
	})
	msgs := []ChatMessage{{Role: "user", Content: "x"}}
	content, _, err := c.Chat(context.Background(), ChatRequest{Messages: msgs, MaxTokens: 10, Task: TaskAudit})
	if err != nil || content == "server" || *got != nil {
		t.Fatalf("audit must go to the team endpoint only: %q %v, server got %s", content, err, *got)
	}
	args := strings.Split(readFile(t, filepath.Join(dir, "args")), "\x00")
	if !strings.Contains(strings.Join(args, " "), "--model claude-opus-5-5 --effort high") {
		t.Fatalf("the CLI must get the model and the effort of the route, args %q", args)
	}
	if err := os.Remove(filepath.Join(dir, "args")); err != nil {
		t.Fatal(err)
	}
	content, _, err = c.Chat(context.Background(), ChatRequest{Messages: msgs, MaxTokens: 10, Task: TaskDetect})
	if err != nil || content != "server" || *got == nil {
		t.Fatalf("detect must go to the server: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); err == nil {
		t.Fatal("detect must not start the CLI")
	}
}
