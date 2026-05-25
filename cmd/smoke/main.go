package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	codexauth "github.com/mattsp1290/codex-auth-go"
)

func main() {
	ctx := context.Background()
	appName := os.Getenv("CODEX_AUTH_APPNAME")
	if appName == "" {
		appName = "codex"
	}

	auth := codexauth.NewClient(codexauth.Options{AppName: appName})
	status, err := auth.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if !status.LoggedIn {
		if _, err := auth.Login(ctx, false); err != nil {
			log.Fatal(err)
		}
	}

	client, err := auth.HTTPClient(ctx)
	if errors.Is(err, codexauth.ErrNotLoggedIn) {
		log.Fatal("not logged in after login flow")
	}
	if err != nil {
		log.Fatal(err)
	}

	body := bytes.NewBufferString(`{"model":"gpt-5.5","input":"Respond with ok."}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexauth.CodexEndpoint, body)
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("close response body: %v", err)
		}
	}()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		log.Fatalf("smoke failed: got HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		log.Fatalf("smoke failed: got HTTP %d", resp.StatusCode)
	}
	fmt.Printf("smoke ok: HTTP %d\n", resp.StatusCode)
}
