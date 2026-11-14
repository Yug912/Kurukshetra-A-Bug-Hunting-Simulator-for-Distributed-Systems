.PHONY: build test test-integration lint images sample web clean

build:
	go build -o bin/kuru ./cmd/kuru

test:
	go test ./...

test-integration:
	go test -tags integration ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else go vet ./...; fi

images:
	docker build -t kuru/fake-node -f cmd/fake-node/Dockerfile .
	@if [ -f targets/toykv/Dockerfile ]; then docker build -t kuru/toykv -f targets/toykv/Dockerfile .; fi

sample:
	go run ./cmd/gen-sample

web:
	@if [ -f web/timeline/package.json ]; then cd web/timeline && npm ci && npm run build; else echo "web/timeline/package.json not found; skipping"; fi

clean:
	rm -rf bin runs web/timeline/dist
