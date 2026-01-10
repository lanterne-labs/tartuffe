.PHONY: help test test-short db-reset db-status db-start db-stop setup-test-env

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
	@supabase status --output env 2>/dev/null | grep '^DB_URL=' | sed 's/^DB_URL=//' || echo "Supabase not running. Run 'make db-start' first."

# Run tests with proper environment variables
# Extract DATABASE_URL from supabase CLI and run tests
test:
	@echo "=================================="
	@echo "Running tests for project: $(PROJECT_ID)"
	@echo "=================================="
	@echo "Starting Supabase (if not already running)..."
	@supabase start 2>/dev/null || true
	@echo "Running tests with Supabase DATABASE_URL (port $(SUPABASE_DB_PORT))..."
	@export DATABASE_URL=$$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//') && \
	echo "DATABASE_URL: $$DATABASE_URL" && \
	if [ -f .env ]; then \
		export $$(cat .env | grep -v '^#' | grep -v '^$$' | xargs) && \
		echo "Loaded additional variables from .env" && \
		go test -v ./...; \
	else \
		echo "No .env file found, running tests with DATABASE_URL only" && \
		go test -v ./...; \
	fi

# Run tests without verbose output
test-short:
	@echo "Starting Supabase (if not already running)..."
	@supabase start 2>/dev/null || true
	@echo "Running tests for project '$(PROJECT_ID)'..."
	@export DATABASE_URL=$$(supabase status --output env | grep '^DB_URL=' | sed 's/^DB_URL=//') && \
	if [ -f .env ]; then \
		export $$(cat .env | grep -v '^#' | grep -v '^$$' | xargs) && \
		go test ./...; \
	else \
		go test ./...; \
	fi

