.PHONY: build fmt test race check clean

# Match what the release workflow ships, so a locally built binary behaves like
# a released one: static, stripped, and without the build machine's paths baked
# in. Nothing is stamped with -ldflags -X: the toolchain already records the
# commit, its timestamp, whether the tree was dirty, and since Go 1.24 the
# version from a VCS tag, and --version reads all of it from the build info.
LDFLAGS := -s -w

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
