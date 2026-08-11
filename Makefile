.PHONY: build test race check clean

build:
	go build ./cmd/dirstat

test:
	go test ./...

race:
	go test -race ./...

check:
	gofmt -w $$(find . -name '*.go' -type f)
	go vet ./...

clean:
	rm -f dirstat
