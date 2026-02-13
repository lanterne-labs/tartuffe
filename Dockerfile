# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o dm .

# Final stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /app/dm /usr/local/bin/dm
COPY --from=builder /app/supabase/migrations /migrations

ENV MIGRATIONS_DIR=/migrations

ENTRYPOINT ["dm"]
CMD ["web", "serve"]
