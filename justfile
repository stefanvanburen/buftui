# https://just.systems

[parallel]
@default: lint test

test:
    go test -race ./...

lint:
    go tool honnef.co/go/tools/cmd/staticcheck ./...
