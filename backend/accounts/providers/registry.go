// Package providers are the ways to prove who one is to the site. A provider
// checks what the browser brings back (an ID token for google, a pseudonym
// for noop, a zero-knowledge proof for zkpassport) and names the identity it
// proves. An account provider (google) opens an account; a verification
// provider (zkpassport) proves a document, which the accounts package
// attaches to the signed-in account. Nothing else about the person is kept.
package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// SessionTTL is how long a session stays valid after sign-in.
const SessionTTL = 30 * 24 * time.Hour

// Identity is what a provider vouches for: Subject is unique per person within
// the provider, so (Provider, Subject) names one app user.
type Identity struct {
	Provider    string
	Subject     string
	DisplayName string
}

// ErrInvalidProof is returned (wrapped) by a provider when the payload does
// not prove an identity: a bad request, not a server failure.
var ErrInvalidProof = errors.New("invalid proof")

// Provider is one way to sign in.
type Provider interface {
	// Name is the provider's key in the API paths and in app_users.provider.
	Name() string
	// Info is what the frontend needs to start the sign-in (shown as is).
	Info() ProviderInfo
	// Verify checks the payload the browser sends after the user went through
	// the provider and returns the identity it proves.
	Verify(ctx context.Context, payload json.RawMessage) (Identity, error)
}

// Provider roles.
const (
	// RoleAccount providers open accounts (google).
	RoleAccount = "account"
	// RoleVerification providers prove one document per account (zkpassport).
	RoleVerification = "verification"
	// RoleTest providers do both, with no proof (noop): never where the
	// results must mean something.
	RoleTest = "test"
)

// ProviderInfo describes a provider to the frontend.
type ProviderInfo struct {
	Name string `json:"name"`
	// Role is RoleAccount, RoleVerification or RoleTest.
	Role string `json:"role"`
	// Verified is true when the provider proves one real person per document
	// (zkpassport outside dev mode).
	Verified bool `json:"verified"`
	// Config is provider-specific public configuration for the frontend.
	Config map[string]any `json:"config,omitempty"`
}

// Registry holds the enabled providers by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry enables the given providers.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range providers {
		r.providers[p.Name()] = p
	}
	return r
}

// Get returns the enabled provider of that name.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[name]
	return p, ok
}

// Opens says the provider may open an account.
func Opens(p Provider) bool {
	r := p.Info().Role
	return r == RoleAccount || r == RoleTest
}

// Verifies says the provider may attach a document to an account.
func Verifies(p Provider) bool {
	r := p.Info().Role
	return r == RoleVerification || r == RoleTest
}

// Infos lists the enabled providers, verified ones first, then by name.
func (r *Registry) Infos() []ProviderInfo {
	infos := []ProviderInfo{}
	if r == nil {
		return infos
	}
	for _, p := range r.providers {
		infos = append(infos, p.Info())
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Verified != infos[j].Verified {
			return infos[i].Verified
		}
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// Factory builds a provider from the environment.
type Factory func() (Provider, error)

// factories are the providers AUTH_PROVIDERS may name.
var factories = map[string]Factory{
	NoopName: func() (Provider, error) { return Noop{}, nil },
}

// Register adds a provider factory; providers in other files register in init.
func Register(name string, f Factory) {
	factories[name] = f
}

// RegistryFromEnv enables the providers named in AUTH_PROVIDERS
// (comma-separated). Unset, it enables noop for local development, and none
// on the host (DM_PRODUCTION set), where voting stays closed until the
// providers are named.
func RegistryFromEnv() (*Registry, error) {
	names, ok := os.LookupEnv("AUTH_PROVIDERS")
	if !ok {
		if os.Getenv("DM_PRODUCTION") != "" {
			names = ""
		} else {
			names = NoopName
		}
	}
	var providers []Provider
	for _, n := range strings.Split(names, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		f, ok := factories[n]
		if !ok {
			return nil, fmt.Errorf("AUTH_PROVIDERS: unknown provider %q", n)
		}
		p, err := f()
		if err != nil {
			return nil, fmt.Errorf("auth provider %s: %w", n, err)
		}
		providers = append(providers, p)
	}
	return NewRegistry(providers...), nil
}

// NewSessionToken returns a random session token for the client and the hash
// the database keeps.
func NewSessionToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the stored form of a session token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
