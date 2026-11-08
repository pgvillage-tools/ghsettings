REPO_PATH=github.com/pgvillage-tools/ghsettings
VERSION ?= $(shell scripts/git-version.sh)
LD_FLAGS="-w -X main.version=$(VERSION)"

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

.PHONY: all
all: build

.PHONY: build
build:
	CGO_ENABLED=0 go build -ldflags $(LD_FLAGS) -o bin/ghsettings $(REPO_PATH)/cmd/ghsettings

.PHONY: test
test:
	go test ./... -coverprofile cover.out -coverpkg=./internal/...

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: install-go-test-coverage
install-go-test-coverage:
	go install github.com/vladopajic/go-test-coverage/v2@latest

.PHONY: check-coverage
check-coverage: install-go-test-coverage test
	$(GOBIN)/go-test-coverage --config=./.testcoverage.yaml
