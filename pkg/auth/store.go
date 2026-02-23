package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type AuthCredential struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	AccountID     string    `json:"account_id,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Provider      string    `json:"provider"`
	AuthMethod    string    `json:"auth_method"`
	Email         string    `json:"email,omitempty"`
	ProjectID     string    `json:"project_id,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	Priority      int       `json:"priority,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	LastGoodAt    time.Time `json:"last_good_at,omitempty"`
	LastFailureAt time.Time `json:"last_failure_at,omitempty"`
	FailureCount  int       `json:"failure_count,omitempty"`
	Disabled      bool      `json:"disabled,omitempty"`
}

type AuthStore struct {
	Credentials   map[string]*AuthCredential            `json:"credentials"` // legacy active credential
	Profiles      map[string]map[string]*AuthCredential `json:"profiles,omitempty"`
	ActiveProfile map[string]string                     `json:"active_profile,omitempty"`
}

func (c *AuthCredential) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *AuthCredential) NeedsRefresh() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

func authFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kingclaw", "auth.json")
}

func LoadStore() (*AuthStore, error) {
	path := authFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{
				Credentials:   make(map[string]*AuthCredential),
				Profiles:      make(map[string]map[string]*AuthCredential),
				ActiveProfile: make(map[string]string),
			}, nil
		}
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Credentials == nil {
		store.Credentials = make(map[string]*AuthCredential)
	}
	if store.Profiles == nil {
		store.Profiles = make(map[string]map[string]*AuthCredential)
	}
	if store.ActiveProfile == nil {
		store.ActiveProfile = make(map[string]string)
	}
	normalizeStore(&store)
	return &store, nil
}

func SaveStore(store *AuthStore) error {
	path := authFilePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func GetCredential(provider string) (*AuthCredential, error) {
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	return selectCredential(store, provider), nil
}

func SetCredential(provider string, cred *AuthCredential) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	if cred == nil {
		return fmt.Errorf("credential is nil")
	}
	if cred.Profile == "" {
		cred.Profile = "default"
	}
	store.Credentials[provider] = cred
	if store.Profiles[provider] == nil {
		store.Profiles[provider] = make(map[string]*AuthCredential)
	}
	store.Profiles[provider][cred.Profile] = cred
	store.ActiveProfile[provider] = cred.Profile
	return SaveStore(store)
}

func DeleteCredential(provider string) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	delete(store.Credentials, provider)
	delete(store.Profiles, provider)
	delete(store.ActiveProfile, provider)
	return SaveStore(store)
}

func DeleteAllCredentials() error {
	path := authFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func MarkCredentialSuccess(provider, profile string) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	cred := getProfileCredential(store, provider, profile)
	if cred == nil {
		return nil
	}
	cred.LastGoodAt = time.Now()
	cred.CooldownUntil = time.Time{}
	cred.LastFailureAt = time.Time{}
	cred.FailureCount = 0
	store.Credentials[provider] = cred
	return SaveStore(store)
}

func MarkCredentialFailure(provider, profile string) error {
	store, err := LoadStore()
	if err != nil {
		return err
	}
	cred := getProfileCredential(store, provider, profile)
	if cred == nil {
		return nil
	}
	cred.FailureCount++
	cred.LastFailureAt = time.Now()
	cred.CooldownUntil = time.Now().Add(failureCooldown(cred.FailureCount))
	store.Credentials[provider] = selectCredential(store, provider)
	return SaveStore(store)
}

func failureCooldown(failureCount int) time.Duration {
	switch {
	case failureCount <= 1:
		return 1 * time.Minute
	case failureCount == 2:
		return 5 * time.Minute
	case failureCount == 3:
		return 15 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func normalizeStore(store *AuthStore) {
	for provider, cred := range store.Credentials {
		if cred == nil {
			continue
		}
		if cred.Profile == "" {
			cred.Profile = "default"
		}
		if store.Profiles[provider] == nil {
			store.Profiles[provider] = make(map[string]*AuthCredential)
		}
		store.Profiles[provider][cred.Profile] = cred
		if store.ActiveProfile[provider] == "" {
			store.ActiveProfile[provider] = cred.Profile
		}
	}
}

func getProfileCredential(store *AuthStore, provider, profile string) *AuthCredential {
	if profile != "" && store.Profiles[provider] != nil {
		if c := store.Profiles[provider][profile]; c != nil {
			return c
		}
	}
	return selectCredential(store, provider)
}

func selectCredential(store *AuthStore, provider string) *AuthCredential {
	profiles := store.Profiles[provider]
	if len(profiles) == 0 {
		return store.Credentials[provider]
	}
	now := time.Now()
	candidates := make([]*AuthCredential, 0, len(profiles))
	for _, cred := range profiles {
		if cred == nil || cred.Disabled || cred.AccessToken == "" {
			continue
		}
		if !cred.CooldownUntil.IsZero() && now.Before(cred.CooldownUntil) {
			continue
		}
		candidates = append(candidates, cred)
	}
	if len(candidates) == 0 {
		for _, cred := range profiles {
			if cred != nil && !cred.Disabled && cred.AccessToken != "" {
				candidates = append(candidates, cred)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.LastGoodAt.Equal(b.LastGoodAt) {
			return a.LastGoodAt.After(b.LastGoodAt)
		}
		if a.NeedsRefresh() != b.NeedsRefresh() {
			return !a.NeedsRefresh()
		}
		return a.Profile < b.Profile
	})
	selected := candidates[0]
	store.Credentials[provider] = selected
	store.ActiveProfile[provider] = selected.Profile
	return selected
}
