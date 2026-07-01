# https://just.systems

@default: lint test

test:
    go test ./...

lint:
    go tool honnef.co/go/tools/cmd/staticcheck ./...
