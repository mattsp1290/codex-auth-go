package codexauth_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"

	codexauth "github.com/mattsp1290/codex-auth-go"
)

func ExampleNewClient() {
	if false {
		ctx := context.Background()
		auth := codexauth.NewClient(codexauth.Options{AppName: "yourapp"})

		status, err := auth.Status(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if !status.LoggedIn {
			if _, err := auth.Login(ctx, false); err != nil {
				log.Fatal(err)
			}
		}

		httpClient, err := auth.HTTPClient(ctx)
		if errors.Is(err, codexauth.ErrNotLoggedIn) {
			log.Fatal("not logged in")
		}
		if err != nil {
			log.Fatal(err)
		}

		body := bytes.NewBufferString(`{"model":"gpt-5.5","input":"hello"}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexauth.CodexEndpoint, body)
		if err != nil {
			log.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")

		_ = httpClient
		_ = req
	}

	// Output:
}

func ExampleClient_ListModels() {
	if false {
		ctx := context.Background()
		auth := codexauth.NewClient(codexauth.Options{AppName: "yourapp"})
		models, err := auth.ListModels(ctx, "your-catalog-compatibility-value")
		if errors.Is(err, codexauth.ErrNotLoggedIn) {
			log.Fatal("not logged in")
		}
		if err != nil {
			log.Fatal(err)
		}
		for _, model := range models {
			_ = model.Slug
			_ = model.SupportedReasoningEfforts
		}
	}

	// Output:
}
