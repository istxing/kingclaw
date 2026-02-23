package auth

import (
	"testing"
	"time"
)

func TestGetCredential_SelectsAvailableProfileByPriority(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_ = SetCredential("openai", &AuthCredential{
		AccessToken: "low-pri",
		Provider:    "openai",
		AuthMethod:  "oauth",
		Profile:     "p1",
		Priority:    1,
	})
	_ = SetCredential("openai", &AuthCredential{
		AccessToken: "high-pri",
		Provider:    "openai",
		AuthMethod:  "oauth",
		Profile:     "p2",
		Priority:    10,
	})

	cred, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential err: %v", err)
	}
	if cred == nil || cred.AccessToken != "high-pri" {
		t.Fatalf("expected high priority profile, got %#v", cred)
	}
}

func TestMarkCredentialFailureAndSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_ = SetCredential("openai", &AuthCredential{
		AccessToken: "tok",
		Provider:    "openai",
		AuthMethod:  "oauth",
		Profile:     "default",
	})

	if err := MarkCredentialFailure("openai", "default"); err != nil {
		t.Fatalf("MarkCredentialFailure err: %v", err)
	}
	cred, _ := GetCredential("openai")
	if cred == nil {
		t.Fatal("credential should exist")
	}
	if cred.FailureCount < 1 {
		t.Fatalf("expected failure count >=1, got %d", cred.FailureCount)
	}
	if cred.CooldownUntil.Before(time.Now()) {
		t.Fatalf("expected cooldown in future, got %v", cred.CooldownUntil)
	}

	if err := MarkCredentialSuccess("openai", "default"); err != nil {
		t.Fatalf("MarkCredentialSuccess err: %v", err)
	}
	cred, _ = GetCredential("openai")
	if cred.FailureCount != 0 {
		t.Fatalf("expected failure count reset, got %d", cred.FailureCount)
	}
	if !cred.CooldownUntil.IsZero() {
		t.Fatalf("expected cooldown cleared, got %v", cred.CooldownUntil)
	}
}
