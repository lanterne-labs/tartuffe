package llm

import (
	"fmt"
	"strings"
	"sync"
)

// Task names the job of one LLM call. ChatRequest.Task selects the thinking
// setting of the call (see thinkingTasks); the server never sees it.
type Task string

// The tasks with a thinking setting. A call without a Task never thinks.
const (
	TaskOccurrence    Task = "occurrence"     // grouping gate: two facts, one occurrence?
	TaskSameClaim     Task = "same-claim"     // dedupe drop gate: one claim reworded?
	TaskDedupBatch    Task = "dedup-batch"    // a batch of facts: duplicate groups and fact quality
	TaskExtract       Task = "extract"        // fact extraction from a document
	TaskStanceAnalyze Task = "stance-analyze" // new fact against a stance: duplicate, contradiction
	TaskStanceAssign  Task = "stance-assign"  // which stance a fact belongs to
	TaskStanceLabel   Task = "stance-label"   // stance labels and summaries
	TaskStanceMerge   Task = "stance-merge"   // two stances, one subject?
	TaskDetect        Task = "detect"         // batch contradiction detection
	TaskVerify        Task = "verify"         // refute pass on a flagged contradiction
	TaskAudit         Task = "audit"          // auditor of contradictions and facts
	TaskBundle        Task = "bundle"         // contradiction belongs to a bundle?
	TaskEntityDedup   Task = "entity-dedup"   // two entity names, one entity?
	TaskEntityFilter  Task = "entity-filter"  // is the name a real entity?
	TaskComplaint     Task = "complaint"      // complaint triage
	TaskHandle        Task = "handle"         // a person's social-media handle, or none
)

// AllTasks lists every task, for flag parsing and documentation.
var AllTasks = []Task{
	TaskOccurrence, TaskSameClaim, TaskDedupBatch, TaskExtract, TaskStanceAnalyze, TaskStanceAssign,
	TaskStanceLabel, TaskStanceMerge, TaskDetect, TaskVerify, TaskAudit, TaskBundle,
	TaskEntityDedup, TaskEntityFilter, TaskComplaint, TaskHandle,
}

// thinkingTasks are the tasks that think in production. A task thinks only
// when its golden shows a gain, and the nightly must still fit its window: a
// thinking call costs about 3 times a plain call (CLAUDE.md "Local LLM").
var thinkingTasks = map[Task]bool{
	TaskOccurrence: true, // 207 labelled pairs: 87 % -> 95 % of true groups, 2 % clear errors
}

var (
	thinkingMu       sync.RWMutex
	thinkingOverride map[Task]bool // nil: production table
)

// Thinks reports whether calls of task t think.
func Thinks(t Task) bool {
	thinkingMu.RLock()
	defer thinkingMu.RUnlock()
	if thinkingOverride != nil {
		return thinkingOverride[t]
	}
	return thinkingTasks[t]
}

// OverrideThinking replaces the production table with the given set of
// thinking tasks until restore is called. It is for the goldens, to measure a
// task with and without thinking (llmtest flag -llm-think).
func OverrideThinking(tasks []Task) (restore func()) {
	set := make(map[Task]bool, len(tasks))
	for _, t := range tasks {
		set[t] = true
	}
	thinkingMu.Lock()
	prev := thinkingOverride
	thinkingOverride = set
	thinkingMu.Unlock()
	return func() {
		thinkingMu.Lock()
		thinkingOverride = prev
		thinkingMu.Unlock()
	}
}

// ParseTasks parses a comma-separated task list; "none" is the empty list.
func ParseTasks(s string) ([]Task, error) {
	s = strings.TrimSpace(s)
	if s == "none" {
		return []Task{}, nil
	}
	known := make(map[Task]bool, len(AllTasks))
	for _, t := range AllTasks {
		known[t] = true
	}
	var out []Task
	for _, part := range strings.Split(s, ",") {
		t := Task(strings.TrimSpace(part))
		if !known[t] {
			return nil, fmt.Errorf("unknown LLM task %q (known: %v, or none)", t, AllTasks)
		}
		out = append(out, t)
	}
	return out, nil
}

// applyThinking turns thinking on for a request whose task thinks: the
// chat-template switch, a thinking budget, and room for it on top of the
// answer's own MaxTokens.
func applyThinking(req *ChatRequest) {
	if req.Task == "" || !Thinks(req.Task) {
		return
	}
	req.ChatTemplateKwargs = thinkingKwargs()
	req.ThinkingBudgetTokens = ThinkingBudgetTokens
	req.MaxTokens += ThinkingBudgetTokens
}

// ReservedTokens returns the output tokens a call of task reserves when its
// answer budget is answerTokens: more when the task thinks.
func ReservedTokens(task Task, answerTokens int) int {
	if task != "" && Thinks(task) {
		return answerTokens + ThinkingBudgetTokens
	}
	return answerTokens
}
