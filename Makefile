GO ?= go
GOVULNCHECK_VERSION := v1.9.0
IMAGE ?= blossm-toolbox:local
CONTAINER_NETWORK ?= blossm-go-native_default
CONTAINER_POSTGRES_URL ?= postgres://postgres:postgres@postgres:5432/blossm?sslmode=disable
export GOWORK := off

.PHONY: fmt-check vet test race native native-redis modules build config-check container container-smoke vuln check
fmt-check:
	python3 tools/check_boundaries.py
	@output=$$(gofmt -l .) || exit $$?; test -z "$$output" || { printf '%s\n' "$$output"; echo 'Go files require gofmt'; exit 1; }
vet:
	$(GO) vet -mod=readonly ./...
test:
	$(GO) test -mod=readonly -count=1 ./...
race:
	$(GO) test -mod=readonly -race -count=1 ./...
native:
	@test -n "$$POSTGRES_URL" || { echo 'POSTGRES_URL is required'; exit 1; }
	@test -n "$$MONGODB_URI" || { echo 'MONGODB_URI is required'; exit 1; }
	$(GO) test -mod=readonly -race -p=1 -tags=integration -count=1 -timeout=10m ./...
native-redis:
	@test -n "$$REDIS_ADDR" || { echo 'REDIS_ADDR is required'; exit 1; }
	$(GO) test -mod=readonly -race -tags=redis_integration -count=1 ./integrations/redis
modules:
	$(GO) mod verify
	$(GO) mod tidy -diff
build:
	$(GO) build -mod=readonly -trimpath -o bin/blossm ./cmd/blossm
config-check:
	$(GO) test -mod=readonly ./cli -run 'TestArtifacts|TestStrictConfiguration|TestOfflineCLI|TestCheckedExamples|TestAWSPublicationSizing' -count=1
	$(GO) run -mod=readonly ./cmd/blossm validate --config deploy/examples/gcp-postgres.json
	$(GO) run -mod=readonly ./cmd/blossm validate --config deploy/examples/aws-mongo.json
container:
	docker build --platform linux/amd64 -t $(IMAGE) .
	docker run --rm --platform linux/amd64 --entrypoint /blossm $(IMAGE) version
	$(MAKE) container-smoke
container-smoke:
	CONTAINER_IMAGE=$(IMAGE) $(GO) test -mod=readonly -tags=containerintegration -count=1 -timeout=3m ./integration
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
check: fmt-check vet test race native native-redis modules config-check build vuln container
