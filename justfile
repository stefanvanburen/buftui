# https://just.systems

@default: lint test

test:
    go test ./...

lint:
    go tool honnef.co/go/tools/cmd/staticcheck ./...
    test -z "$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
