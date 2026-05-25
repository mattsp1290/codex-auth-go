package codexauth

import (
	"context"
	"testing"
)

func TestClientsWithDifferentAppNamesUseDistinctCredentialFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	advisor := NewClient(Options{AppName: "advisor"})
	localSymphony := NewClient(Options{AppName: "local-symphony"})

	advisorPath, err := advisor.Path()
	if err != nil {
		t.Fatalf("advisor Path: %v", err)
	}
	localSymphonyPath, err := localSymphony.Path()
	if err != nil {
		t.Fatalf("local-symphony Path: %v", err)
	}
	if advisorPath == localSymphonyPath {
		t.Fatalf("paths are equal: %q", advisorPath)
	}

	if err := advisor.save(AuthFile{OpenAI: &Credentials{
		Access:    "advisor-access",
		Refresh:   "advisor-refresh",
		Expires:   1,
		AccountID: "advisor-account",
	}}); err != nil {
		t.Fatalf("advisor save: %v", err)
	}
	if err := localSymphony.save(AuthFile{OpenAI: &Credentials{
		Access:    "local-access",
		Refresh:   "local-refresh",
		Expires:   2,
		AccountID: "local-account",
	}}); err != nil {
		t.Fatalf("local-symphony save: %v", err)
	}

	advisorAuth, err := advisor.load()
	if err != nil {
		t.Fatalf("advisor load: %v", err)
	}
	localSymphonyAuth, err := localSymphony.load()
	if err != nil {
		t.Fatalf("local-symphony load: %v", err)
	}

	if advisorAuth.OpenAI == nil || advisorAuth.OpenAI.Access != "advisor-access" {
		t.Fatalf("advisor creds = %+v, want advisor-access", advisorAuth.OpenAI)
	}
	if localSymphonyAuth.OpenAI == nil || localSymphonyAuth.OpenAI.Access != "local-access" {
		t.Fatalf("local-symphony creds = %+v, want local-access", localSymphonyAuth.OpenAI)
	}
}

func TestDeprecatedWrapperAndAdvisorClientUseSameCredentialFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	origLoginDeviceFn := loginDeviceFn
	defer func() { loginDeviceFn = origLoginDeviceFn }()

	wrapperCreds := Credentials{Access: "wrapper-access", Refresh: "wrapper-refresh", Expires: 1}
	loginDeviceFn = func(context.Context) (Credentials, error) {
		if err := Save(AuthFile{OpenAI: &wrapperCreds}); err != nil {
			return Credentials{}, err
		}
		return wrapperCreds, nil
	}

	if _, err := Login(context.Background(), true); err != nil {
		t.Fatalf("wrapper Login: %v", err)
	}
	wrapperPath, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}

	advisor := NewClient(Options{AppName: "advisor"})
	clientPath, err := advisor.Path()
	if err != nil {
		t.Fatalf("Client.Path: %v", err)
	}
	if clientPath != wrapperPath {
		t.Fatalf("Client advisor path = %q, wrapper path = %q", clientPath, wrapperPath)
	}

	clientCreds := Credentials{Access: "client-access", Refresh: "client-refresh", Expires: 2}
	advisor.loginDeviceFn = func(context.Context) (Credentials, error) {
		if err := advisor.save(AuthFile{OpenAI: &clientCreds}); err != nil {
			return Credentials{}, err
		}
		return clientCreds, nil
	}

	if _, err := advisor.Login(context.Background(), true); err != nil {
		t.Fatalf("advisor Client Login: %v", err)
	}
	af, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if af.OpenAI == nil || af.OpenAI.Access != "client-access" {
		t.Fatalf("wrapper Load().OpenAI = %+v, want client-access", af.OpenAI)
	}
}
