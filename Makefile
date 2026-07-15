.PHONY: up down test lint build seed seed-fulltext bench clean

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

# Seed the database with real exercise-science abstracts from PubMed
seed:
	python scripts/fetch_pubmed_corpus.py
	python scripts/upload_corpus.py

# Same as seed, but upgrades whichever studies have an Open Access full-text
# copy in PMC first (slower — does an FTP download per eligible study)
seed-fulltext:
	python scripts/fetch_pubmed_corpus.py
	python scripts/fetch_pmc_fulltext.py
	python scripts/upload_corpus.py

# Run load tests
bench:
	k6 run benchmarks/load_test.js

# Clean up
clean:
	docker compose down -v
	rm -rf bin/
