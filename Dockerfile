# ── Build stage ──
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build both binaries as static executables
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /worker ./cmd/worker

# ── API runtime (scratch = ~0 overhead, <20MB image) ──
FROM scratch AS api
COPY --from=builder /api /api
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/migrate.sql /app/migrate.sql
COPY static/ /static/
EXPOSE 8080
ENTRYPOINT ["/api"]

# ── Worker runtime ──
FROM scratch AS worker
COPY --from=builder /worker /worker
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/worker"]
