# buftui

A TUI for the [Buf Schema Registry](https://buf.build).

Built on the [bufbuild/registry](https://buf.build/bufbuild/registry) API,
using [generated SDKs](https://buf.build/docs/bsr/generated-sdks/overview)
for the API definitions and [Connect](https://connectrpc.com) clients,
and using [bubbletea](https://github.com/charmbracelet/bubbletea) for rendering the TUI.

## Usage

```shell
go run github.com/stefanvanburen/buftui@latest
```
