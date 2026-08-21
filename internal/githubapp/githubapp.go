// Package githubapp mints and caches GitHub App installation tokens using
// only the standard library: the RS256 JWT GitHub requires for App
// authentication needs nothing beyond crypto/rsa, crypto/sha256 and
// encoding/pem.
package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// jwtLifetime stays under GitHub's 10 minute cap on App JWTs; a full 10
	// minutes leaves no room for clock drift or transit delay to push the
	// token past the boundary GitHub actually enforces.
	jwtLifetime = 9 * time.Minute

	// clockSkew backdates iat below the current time so a self-hosted clock
	// running a little ahead of GitHub's does not produce a JWT that looks
	// like it was issued in the future, which GitHub rejects outright.
	clockSkew = 60 * time.Second

	// tokenExpiryMargin refreshes the installation token 5 minutes before
	// GitHub actually revokes it, so a dispatch already in flight never
	// races the real expiry mid-request.
	tokenExpiryMargin = 5 * time.Minute

	requestTimeout = 30 * time.Second
)

// TokenSource mints and caches GitHub App installation tokens for the
// runner repository configured on it. A zero installationID means the
// installation ID is unknown up front and must be looked up from that
// repository; a non-zero one is used as-is, which is how a dispatch-only
// App distinct from the one that runs Renovate gets wired in.
type TokenSource struct {
	appID          string
	key            *rsa.PrivateKey
	installationID int64
	owner, repo    string
	apiURL         string
	client         *http.Client
	now            func() time.Time

	mu                     sync.Mutex
	resolvedInstallationID int64
	cachedToken            string
	expiresAt              time.Time
}

// New builds a TokenSource. privateKeyPEM must be an RSA key in either the
// PKCS#1 form ("RSA PRIVATE KEY") GitHub hands out when an App key is
// generated, or PKCS#8 ("PRIVATE KEY"). A bad key is reported here, at
// construction, rather than surfacing later as an opaque signing failure on
// the first dispatch.
func New(apiURL, appID, privateKeyPEM string, installationID int64, runnerRepository string, client *http.Client) (*TokenSource, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	owner, repo, ok := strings.Cut(runnerRepository, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("runner repository %q must be in owner/repo form", runnerRepository)
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &TokenSource{
		appID:          appID,
		key:            key,
		installationID: installationID,
		owner:          owner,
		repo:           repo,
		apiURL:         strings.TrimSuffix(apiURL, "/"),
		client:         client,
		now:            time.Now,
	}, nil
}

// parsePrivateKey accepts both PEM encodings GitHub App owners are likely to
// have on hand: GitHub itself only ever distributes PKCS#1, but PKCS#8 is
// what most "convert my key" tooling and some secret managers produce.
func parsePrivateKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a valid PKCS#1 or PKCS#8 key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want an RSA key", parsed)
	}
	return key, nil
}

// Token returns a valid installation access token, minting or refreshing one
// as needed. It is safe for concurrent use: the mutex serializes refreshes so
// concurrent dispatches never mint two tokens (and issue two billable calls
// to GitHub) for the same brief expiry window.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cachedToken != "" && s.now().Before(s.expiresAt.Add(-tokenExpiryMargin)) {
		return s.cachedToken, nil
	}

	installationID, err := s.installationIDLocked(ctx)
	if err != nil {
		return "", err
	}

	token, expiresAt, err := s.mintToken(ctx, installationID)
	if err != nil {
		return "", err
	}
	s.cachedToken = token
	s.expiresAt = expiresAt
	return token, nil
}

// installationIDLocked returns the installation ID to mint a token for. It
// must be called with s.mu held.
func (s *TokenSource) installationIDLocked(ctx context.Context) (int64, error) {
	if s.installationID != 0 {
		return s.installationID, nil
	}
	if s.resolvedInstallationID != 0 {
		return s.resolvedInstallationID, nil
	}
	id, err := s.lookupInstallationID(ctx)
	if err != nil {
		return 0, err
	}
	s.resolvedInstallationID = id
	return id, nil
}

// lookupInstallationID resolves the installation ID from the runner
// repository, used when GITHUB_APP_INSTALLATION_ID was left unset because
// the same App that runs Renovate is also the one dispatching it.
func (s *TokenSource) lookupInstallationID(ctx context.Context) (int64, error) {
	jwt, err := s.buildJWT()
	if err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/installation", s.apiURL, s.owner, s.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build installation lookup request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	res, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("look up installation for %s/%s: %w", s.owner, s.repo, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		body := readLimited(res.Body)
		return 0, fmt.Errorf("look up installation for %s/%s: github returned %d: %s",
			s.owner, s.repo, res.StatusCode, body)
	}

	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode installation lookup response: %w", err)
	}
	return payload.ID, nil
}

// mintToken exchanges a freshly signed JWT for an installation access token.
func (s *TokenSource) mintToken(ctx context.Context, installationID int64) (string, time.Time, error) {
	jwt, err := s.buildJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", s.apiURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build access token request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	res, err := s.client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint installation access token: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusCreated {
		body := readLimited(res.Body)
		return "", time.Time{}, fmt.Errorf("mint installation access token: github returned %d: %s",
			res.StatusCode, body)
	}

	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", time.Time{}, fmt.Errorf("decode access token response: %w", err)
	}
	return payload.Token, payload.ExpiresAt, nil
}

// buildJWT signs the App-level JWT GitHub requires to call the installation
// lookup and access-token endpoints. GitHub verifies alg/typ/iss/iat/exp
// itself; there is no library to lean on for a 3-field header and 3-claim
// payload, so it is assembled by hand.
func (s *TokenSource) buildJWT() (string, error) {
	now := s.now()

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("encode jwt header: %w", err)
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-clockSkew).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": s.appID,
	})
	if err != nil {
		return "", fmt.Errorf("encode jwt claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func readLimited(body io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(body, 4<<10))
	return strings.TrimSpace(string(b))
}
