.PHONY: up down test lint build seed bench clean

# Start all services
up:
	docker compose up --build -d

# Stop all services
down:
	docker compose down

# Run tests
test:
	go test ./... -v -cover

# Lint Go code
lint:
	go vet ./...
	golangci-lint run ./...

# Build Go binaries locally
build:
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker

# Seed the database with sample documents
seed:
	@echo "TODO: implement bulk ingestion script (Day 6)"

# Run load tests
bench:
	k6 run benchmarks/load_test.js

# Clean up
clean:
	docker compose down -v
	rm -rf bin/
