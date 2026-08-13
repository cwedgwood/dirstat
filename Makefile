.PHONY: build fmt test race check clean

# Match what the release workflow ships, so a locally built binary behaves like
# a released one: static, stripped, and without the build machine's paths baked
# in. VERSION stays empty until a tag exists, which lets the binary fall back to
# the module pseudo-version recorded in the build info rather than reporting a
# bare commit as though it were a release.
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o dirstat ./cmd/dirstat

test:
	go test -count=1 ./...

race:
	go test -count=1 -race ./...

# Reports rather than repairs: this is the check CI runs, and a target that
# silently rewrites the tree cannot reproduce a CI failure. Use `make fmt` to
# actually format.
check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "these files are not gofmt-clean:" >&2; \
		echo "$$unformatted" >&2; \
		exit 1; \
	fi
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f dirstat
