# https://just.systems

@default: lint test

test:
    go test ./...

lint:
    # -printf.funcs names ok.Sprintf so vet checks the format strings inside
    # test assertion options; printf's default list omits it, and go test's
    # built-in vet cannot be passed analyzer flags.
    go vet -printf.funcs=Sprintf ./...
    go tool honnef.co/go/tools/cmd/staticcheck ./...
    test -z "$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)
    go fix -diff ./...
