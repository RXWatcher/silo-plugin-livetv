BINARY := silo-plugin-livetv
GO ?= go
PNPM ?= pnpm

.PHONY: build test test-go test-web web fmt vet clean

build: web
	$(GO) build -o $(BINARY) ./cmd/silo-plugin-livetv
	sha256sum $(BINARY) | awk '{print $$1}' > $(BINARY).sha256

web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run build

test: test-go test-web

test-go:
	$(GO) test ./...

test-web:
	cd web && $(PNPM) run test --run

fmt:
	$(GO) fmt ./...
	# web formatting: prettier is not in devDeps; run from your IDE.

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY) $(BINARY).sha256
	rm -rf web/dist web/node_modules
