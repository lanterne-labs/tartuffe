# Developing Digital Mirror

The guide is `CLAUDE.md`: the commands, the architecture, the conventions,
the deployment. This file keeps only the local workflow.

## Local workflow

```bash
make db-start                       # Supabase (Postgres on 55322, Studio on 55323)
supabase db reset                   # apply every migration from scratch (local only)
go build -o dm .                    # the backend binary
./dm web serve --port 8080          # the API and the SSE stream
cd frontend && npm install && npm run dev -- --port 3000   # the site, against the API on 8080
```

Facts come from the worker's tasks (`./dm collect add "Name" --from 2024-01-01`,
`./dm worker run`), the vote from `./dm vote …` (see CLAUDE.md and
`docs/vote/SPEC.md`). Tests: `make test` (Supabase must run), or one package
with `DATABASE_URL` set as the Makefile does.

## Environment

Copy `.env.example` to `.env`; every variable is documented there.
