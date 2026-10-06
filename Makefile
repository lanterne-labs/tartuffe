.PHONY: help test test-short db-reset db-status db-start db-stop setup-test-env llm-goldens llm-eval

# Project-specific settings
# The project_id from supabase/config.toml - used for container naming
PROJECT_ID := mirror
SUPABASE_DB_PORT := 55322

# Default target
help:
	@echo "Digital Mirror - Supabase & Testing Commands"
	@echo "=============================================="
	@echo "Project: $(PROJECT_ID) (uses ports 55321-55327 to avoid conflicts)"
	@echo ""
	@echo "Available targets:"
	@echo "  make db-start       - Start the Supabase local instance (project: $(PROJECT_ID))"
	@echo "  make db-stop        - Stop the Supabase local instance"
	@echo "  make db-reset       - Reset the local Supabase database with migrations"
	@echo "  make db-status      - Show Supabase database status"
	@echo "  make test           - Run all Go tests with proper DATABASE_URL"
	@echo "  make test-short     - Run tests without verbose output"
	@echo "  make setup-test-env - Show the DATABASE_URL that would be used for tests"
	@echo "  make llm-goldens    - Run the LLM goldens (LLM_API=... LLM_MODEL=... to test another model)"
	@echo "  make llm-eval       - Run the 207-pair grouping eval (about 2 h; same LLM_API / LLM_MODEL)"

# Start Supabase (ensures it's running)
db-start:
	@echo "Starting Supabase instance for project '$(PROJECT_ID)'..."
	@echo "Using DB port: $(SUPABASE_DB_PORT)"
	@supabase start

# Stop Supabase
db-stop:
	@echo "Stopping Supabase instance for project '$(PROJECT_ID)'..."
	@supabase stop

# Reset the Supabase database (runs migrations)
db-reset:
	@echo "Resetting Supabase database for project '$(PROJECT_ID)'..."
	@echo "This will apply all migrations from supabase/migrations/"
	@supabase db reset

# Show database status
db-status:
	@echo "Checking Supabase status for project '$(PROJECT_ID)'..."
	@supabase status

# Show the test environment setup
setup-test-env:
	@echo "Project ID: $(PROJECT_ID)"
	@echo "DB Port: $(SUPABASE_DB_PORT)"
	@echo ""
	@echo "DATABASE_URL will be set to:"
	@supabase status --output env 2>/dev/null | grep '^DB_URL=' | sed -e 's/^DB_URL=//' -e 's/^"//' -e 's/"$$//' || echo "Supabase not running. Run 'make db-start' first."

# Run tests with proper environment variables
# Extract DATABASE_URL from supabase CLI and run tests
# -p 1: run packages one at a time. Six packages hit the local vLLM box; under
# that concurrent batching load the LLM judges' verdicts flip (measured: golden
# cases that pass 3/3 alone fail when the whole suite runs in parallel), so a
# parallel run is red by construction, not by regression. Same reason for the
# 40m timeout — the voted goldens take ~6 min per pass.
test:
	@echo "=================================="
	@echo "Running tests for project: $(PROJECT_ID)"
	@echo "=================================="
	@echo "Starting Supabase (if not already running)..."
	@supabase start 2>/dev/null || true
	@echo "Running tests with Supabase DATABASE_URL (port $(SUPABASE_DB_PORT))..."
	@export DATABASE_URL=$$(supabase status --output env | grep '^DB_URL=' | sed -e 's/^DB_URL=//' -e 's/^"//' -e 's/"$$//') && \
	echo "DATABASE_URL: $$DATABASE_URL" && \
	if [ -f .env ]; then \
		export $$(cat .env | grep -v '^#' | grep -v '^$$' | xargs) && \
		echo "Loaded additional variables from .env" && \
		go test -p 1 -v -timeout 40m ./...; \
	else \
		echo "No .env file found, running tests with DATABASE_URL only" && \
		go test -p 1 -v -timeout 40m ./...; \
	fi

# Run tests without verbose output
test-short:
	@echo "Starting Supabase (if not already running)..."
	@supabase start 2>/dev/null || true
	@echo "Running tests for project '$(PROJECT_ID)'..."
	@export DATABASE_URL=$$(supabase status --output env | grep '^DB_URL=' | sed -e 's/^DB_URL=//' -e 's/^"//' -e 's/"$$//') && \
	if [ -f .env ]; then \
		export $$(cat .env | grep -v '^#' | grep -v '^$$' | xargs) && \
		go test -p 1 -timeout 40m ./...; \
	else \
		go test -p 1 -timeout 40m ./...; \
	fi


# LLM goldens: every judge's golden set against the model of .env, or against
# another server, model or thinking setting for one run (see backend/llmtest):
#   make llm-goldens
#   make llm-goldens LLM_API=http://127.0.0.1:8095/v1 LLM_MODEL=candidate
#   make llm-goldens LLM_THINK=occurrence,stance-analyze,detect   (tasks in backend/llm/task.go)
# Packages run one at a time (one GPU slot). The full log is in
# /tmp/llm-goldens.log; the screen shows one line per golden.
LLM_GOLDEN_PKGS := ./backend/core ./backend/contradiction ./backend/entityvalidator ./backend/categorizer
LLM_GOLDEN_RUN := Golden|TestDetectorJudgesGroupsWithTheirRestatements|TestRemoteLLMExtractor
LLM_ARGS = $(if $(LLM_API),-llm-api=$(LLM_API)) $(if $(LLM_MODEL),-llm-model=$(LLM_MODEL)) $(if $(LLM_THINK),-llm-think=$(LLM_THINK))
llm-goldens:
	@go test -p 1 -count=1 -v -timeout 6h -run '$(LLM_GOLDEN_RUN)' $(LLM_GOLDEN_PKGS) -args $(LLM_ARGS) 2>&1 \
		| tee /tmp/llm-goldens.log \
		| grep -E '^(--- (PASS|FAIL|SKIP): Test[A-Za-z_]+ |ok |FAIL|.*=== [A-Z -]+: )' || true

# The 207-pair grouping eval (backend/core/occurrence_eval_test.go): the
# measure to compare models on grouping. Log in /tmp/llm-eval.log.
llm-eval:
	@go test -tags llmeval -count=1 -v -timeout 8h -run 'TestOccurrenceGateEval' ./backend/core -args $(LLM_ARGS) 2>&1 \
		| tee /tmp/llm-eval.log | grep -E 'OCCURRENCE EVAL|gate error|^(ok|FAIL)' || true
