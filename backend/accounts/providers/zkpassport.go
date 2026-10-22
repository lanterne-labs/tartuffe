package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ZKPassportName verifies an account with the ZKPassport app: the user scans the chip of
// their passport or ID card with their phone, the app proves in zero
// knowledge that the holder is an adult (and of the configured nationality),
// and the proof carries a unique identifier per document for this site and
// scope. No name, number or birth date ever leaves the phone.
const ZKPassportName = "zkpassport"

// verifyTimeout bounds one verification: under the API's 60 s request timeout.
const verifyTimeout = 55 * time.Second

// ChallengeTTL is how long a sign-in challenge stays usable: the time to scan
// the document and generate the proof on the phone.
const ChallengeTTL = 10 * time.Minute

// Challenger is a provider whose proofs must carry a one-time challenge the
// server issued (POST /api/auth/challenge/{provider}). The API consumes the
// challenge named in the payload's "challenge" field before calling Verify.
type Challenger interface {
	Provider
	ChallengeTTL() time.Duration
}

// ZKPassport verifies the proofs with zkverifier/verify.mjs, which runs the
// open-source verifier of @zkpassport/sdk locally (no ZKPassport account or
// API).
type ZKPassport struct {
	// Domain is the site's host name (the frontend's): the proofs are bound
	// to it.
	Domain string
	// Scope names the use case; with Domain it fixes each user's identifier.
	// Changing it gives every user a new identifier (a new account).
	Scope string
	// MinAge is the age the holder proves to be at least (0: no age check).
	MinAge int
	// Nationality is the ISO alpha-3 nationality the holder proves ("" for
	// any).
	Nationality string
	// DevMode accepts the mock documents of the app (country ZKR); never in
	// production.
	DevMode bool
	// RPCURL is an Ethereum RPC for the certificate registry reads (the SDK's
	// public endpoint when empty).
	RPCURL string
	// Node and Script run the verifier.
	Node   string
	Script string
	// Timeout bounds one verification.
	Timeout time.Duration

	// slots bounds the verifications running at once (each is a Node process
	// loading the verifier).
	slotsOnce sync.Once
	slots     chan struct{}
}

// NewZKPassportFromEnv reads the ZKPASSPORT_* settings.
func NewZKPassportFromEnv() (Provider, error) {
	z := &ZKPassport{
		Domain:      os.Getenv("ZKPASSPORT_DOMAIN"),
		Scope:       envOr("ZKPASSPORT_SCOPE", "tartuffe-voter"),
		Nationality: envOr("ZKPASSPORT_NATIONALITY", "FRA"),
		RPCURL:      os.Getenv("ZKPASSPORT_RPC_URL"),
		Node:        envOr("ZKPASSPORT_NODE", "node"),
		Script:      os.Getenv("ZKPASSPORT_VERIFIER"),
		Timeout:     verifyTimeout,
	}
	if z.Domain == "" {
		return nil, fmt.Errorf("ZKPASSPORT_DOMAIN is required: the host name of the site (for instance tartuffe.me)")
	}
	age, err := strconv.Atoi(envOr("ZKPASSPORT_MIN_AGE", "18"))
	if err != nil || age < 0 {
		return nil, fmt.Errorf("ZKPASSPORT_MIN_AGE: %q is not an age", os.Getenv("ZKPASSPORT_MIN_AGE"))
	}
	z.MinAge = age
	if v := os.Getenv("ZKPASSPORT_DEV_MODE"); v != "" {
		if z.DevMode, err = strconv.ParseBool(v); err != nil {
			return nil, fmt.Errorf("ZKPASSPORT_DEV_MODE: %w", err)
		}
	}
	if z.Script == "" {
		// The image installs the verifier at /app/zkverifier; a local run
		// uses the repository's copy.
		z.Script = "zkverifier/verify.mjs"
		if _, err := os.Stat("/app/zkverifier/verify.mjs"); err == nil {
			z.Script = "/app/zkverifier/verify.mjs"
		}
	}
	if _, err := os.Stat(z.Script); err != nil {
		return nil, fmt.Errorf("zkpassport verifier %s: %w (run npm ci in zkverifier/)", z.Script, err)
	}
	if _, err := exec.LookPath(z.Node); err != nil {
		return nil, fmt.Errorf("zkpassport verifier needs Node.js (%s): %w", z.Node, err)
	}
	if z.DevMode {
		log.Warn().Msg("ZKPassport dev mode: mock documents are accepted, never use it in production")
	}
	return z, nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func init() {
	Register(ZKPassportName, NewZKPassportFromEnv)
}

func (z *ZKPassport) Name() string { return ZKPassportName }

func (z *ZKPassport) ChallengeTTL() time.Duration { return ChallengeTTL }

// Info gives the frontend what it needs to build the same request as the
// verifier rebuilds.
func (z *ZKPassport) Info() ProviderInfo {
	return ProviderInfo{Name: ZKPassportName, Role: RoleVerification, Verified: !z.DevMode, Config: map[string]any{
		"domain":      z.Domain,
		"scope":       z.Scope,
		"min_age":     z.MinAge,
		"nationality": z.Nationality,
		"dev_mode":    z.DevMode,
	}}
}

// zkVerifierOutput is the verifier's answer.
type zkVerifierOutput struct {
	Verified         bool            `json:"verified"`
	UniqueIdentifier string          `json:"uniqueIdentifier"`
	Errors           json.RawMessage `json:"errors"`
}

// Verify reads {"challenge", "proofs", "result"} (the challenge is already
// consumed by the API) and runs the verifier on it.
func (z *ZKPassport) Verify(ctx context.Context, payload json.RawMessage) (Identity, error) {
	var p struct {
		Challenge string          `json:"challenge"`
		Proofs    json.RawMessage `json:"proofs"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidProof, err)
	}
	if p.Challenge == "" || len(p.Proofs) == 0 || len(p.Result) == 0 {
		return Identity{}, fmt.Errorf("%w: challenge, proofs and result are required", ErrInvalidProof)
	}
	in, err := json.Marshal(map[string]any{
		"proofs":      p.Proofs,
		"result":      p.Result,
		"challenge":   p.Challenge,
		"domain":      z.Domain,
		"scope":       z.Scope,
		"minAge":      z.MinAge,
		"nationality": z.Nationality,
		"devMode":     z.DevMode,
		"rpcUrl":      z.RPCURL,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("zkpassport verifier input: %w", err)
	}
	out, err := z.run(ctx, in)
	if err != nil {
		return Identity{}, err
	}
	if !out.Verified || out.UniqueIdentifier == "" {
		return Identity{}, fmt.Errorf("%w: the passport proof did not verify (%s)", ErrInvalidProof, strings.TrimSpace(string(out.Errors)))
	}
	return Identity{Provider: ZKPassportName, Subject: out.UniqueIdentifier}, nil
}

func (z *ZKPassport) run(ctx context.Context, in []byte) (*zkVerifierOutput, error) {
	z.slotsOnce.Do(func() { z.slots = make(chan struct{}, 2) })
	select {
	case z.slots <- struct{}{}:
		defer func() { <-z.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	timeout := z.Timeout
	if timeout == 0 {
		timeout = verifyTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, z.Node, z.Script)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("zkpassport verifier: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out zkVerifierOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("zkpassport verifier output %q: %w", stdout.String(), err)
	}
	log.Info().Bool("verified", out.Verified).Dur("took", time.Since(start)).Msg("zkpassport proofs checked")
	return &out, nil
}
