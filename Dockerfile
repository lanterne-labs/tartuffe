# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o dm .

# Claude Code CLI stage. The team endpoint (LLM_PROFILE=claude-team) runs
# `claude -p`. The npm package installs the native binary of the platform
# (linux-x64-musl here) at bin/claude.exe. The binary needs only the musl
# libc, so the final stage copies it without Node.
FROM node:22-alpine AS claude
RUN npm install -g @anthropic-ai/claude-code@2.1.296

# Final stage
FROM alpine:3.19

# bash: alpine ships only busybox /bin/sh, which the host's `host ssh`
# shell-probe fails to detect (reports "no shell (bash or sh)"). Debian-based
# service images like postgres have bash and ssh fine, so install it here.
RUN apk add --no-cache ca-certificates tzdata bash

COPY --from=builder /app/dm /usr/local/bin/dm
COPY --from=claude /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /usr/local/bin/claude
RUN claude --version
COPY --from=builder /app/supabase/migrations /migrations

ENV MIGRATIONS_DIR=/migrations

# No ENTRYPOINT: a raw-binary entrypoint (["dm"]) makes the host's `host ssh`
# shell-probe report "container does not have a shell" (it routes the probe
# through the entrypoint). Putting the full command in CMD keeps `dm web serve`
# as the default while letting `host ssh` find /bin/sh. The host service
# "Start Command" overrides CMD entirely, so the worker (`dm worker run`) and
# any other service are unaffected.
CMD ["dm", "web", "serve"]
