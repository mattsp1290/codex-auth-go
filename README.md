# codex-auth-go

Go library for authenticating against the ChatGPT subscription-backed Codex
endpoint and producing an `*http.Client` that can call:

```text
https://chatgpt.com/backend-api/codex/responses
```

This repository is being extracted from `advisor` in small reviewable steps.
PR1 contains only the module skeleton, CI, linting, and dependency pins. The
actual `codexauth` package implementation lands in the follow-up source lift.

## Intended Usage

The v0.1.0 API is centered on an explicit client so multiple consumers can keep
separate credential stores:

```go
package main

import (
	"context"
	"log"

	codexauth "github.com/mattsp1290/codex-auth-go"
)

func main() {
	ctx := context.Background()

	auth := codexauth.NewClient(codexauth.Options{
		AppName: "my-agent",
	})

	httpClient, err := auth.HTTPClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	_ = httpClient
}
```

`Options.AppName` controls the on-disk credential directory. An empty app name
defaults to `codex` for new clients. Deprecated package-level compatibility
functions will keep using `advisor` so existing advisor installations continue
to find their current `auth.json` without a re-login.

## OAuth Constants

The module intentionally uses the public Codex CLI OAuth client ID:

```text
app_EMoamEEZ73f0CkXaXp7hrann
```

That client ID is not configurable. It is the client identifier OpenAI uses for
the Codex subscription audience; changing it breaks authentication for this
endpoint.

The initial release also preserves the existing `originator: advisor` wire
header from the source package. Treat that value as part of the v0.1.0 endpoint
contract unless you have a wire trace proving a different value is accepted.

## Development

CI runs on Linux and executes:

- `go build ./...`
- `go test -race ./...`
- `go vet ./...`
- `GOOS=darwin go vet ./...`
- `GOOS=windows go vet ./...`
- `golangci-lint run`
- `govulncheck ./...`

The cross-compile vet steps keep the OS-specific browser and credential-path
files compiling even though the native test runner is Linux-only.
