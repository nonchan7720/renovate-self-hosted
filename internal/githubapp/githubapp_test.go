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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testKey generates a small-but-valid RSA key once per test binary run; 2048
// bits is slow enough that sharing it across subtests matters.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block))
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

func TestNewAcceptsPKCS1AndPKCS8(t *testing.T) {
	key := testKey(t)

	for name, pemData := range map[string]string{
		"pkcs1": pkcs1PEM(key),
		"pkcs8": pkcs8PEM(t, key),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New("https://api.github.com", "app-id", pemData, 0, "acme/runner", nil); err != nil {
				t.Fatalf("New() = %v, want no error", err)
			}
		})
	}
}

func TestNewRejectsInvalidPEM(t *testing.T) {
	for name, pemData := range map[string]string{
		"not pem at all":  "this is not a pem block",
		"pem wrong bytes": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("garbage")})),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New("https://api.github.com", "app-id", pemData, 0, "acme/runner", nil); err == nil {
				t.Fatal("New() = nil, want an error for an invalid key")
			}
		})
	}
}

// decodeAndVerifyJWT parses the compact JWT sent by the source under test,
// verifies its RS256 signature against pub, and returns the decoded claims.
func decodeAndVerifyJWT(t *testing.T, token string, pub *rsa.PublicKey) map[string]any {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q does not have 3 dot-separated parts", token)
	}

	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "RS256" {
		t.Errorf("alg = %q, want RS256", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Errorf("typ = %q, want JWT", header["typ"])
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := verifyRS256(pub, parts[0]+"."+parts[1], sig); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}

	return claims
}

func TestTokenSendsAVerifiableJWT(t *testing.T) {
	key := testKey(t)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "installation-token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	src, err := New(srv.URL, "my-app-id", pkcs1PEM(key), 42, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if tok != "installation-token" {
		t.Errorf("Token() = %q, want %q", tok, "installation-token")
	}

	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("Authorization = %q, want a Bearer JWT", gotAuth)
	}
	jwt := strings.TrimPrefix(gotAuth, "Bearer ")
	claims := decodeAndVerifyJWT(t, jwt, &key.PublicKey)

	if claims["iss"] != "my-app-id" {
		t.Errorf("iss = %v, want my-app-id", claims["iss"])
	}

	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))
	now := time.Now().Unix()

	if iat >= now {
		t.Errorf("iat = %d, want it in the past (clock-skew backdating) relative to now = %d", iat, now)
	}
	if now-iat > 120 {
		t.Errorf("iat = %d is more than 2 minutes before now = %d, want ~60s", iat, now)
	}
	if exp-iat > 600 {
		t.Errorf("exp-iat = %ds, want at most GitHub's 600s (10 minute) cap", exp-iat)
	}
	if exp <= now {
		t.Errorf("exp = %d, want it in the future", exp)
	}
}

func TestTokenLooksUpInstallationIDWhenUnset(t *testing.T) {
	key := testKey(t)

	var lookupCalls, mintCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			lookupCalls.Add(1)
			if want := "/repos/acme/runner/installation"; r.URL.Path != want {
				t.Errorf("lookup path = %q, want %q", r.URL.Path, want)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			mintCalls.Add(1)
			if want := "/app/installations/99/access_tokens"; r.URL.Path != want {
				t.Errorf("mint path = %q, want %q", r.URL.Path, want)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "tok",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 0, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if lookupCalls.Load() != 1 {
		t.Errorf("lookup called %d times, want 1", lookupCalls.Load())
	}
	if mintCalls.Load() != 1 {
		t.Errorf("mint called %d times, want 1", mintCalls.Load())
	}
}

func TestTokenSkipsLookupWhenInstallationIDSet(t *testing.T) {
	key := testKey(t)

	var lookupCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/installation") {
			lookupCalls.Add(1)
			t.Errorf("lookup called for path %q, want it skipped since installation ID was configured", r.URL.Path)
			return
		}
		if want := "/app/installations/42/access_tokens"; r.URL.Path != want {
			t.Errorf("mint path = %q, want %q", r.URL.Path, want)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "tok",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 42, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if lookupCalls.Load() != 0 {
		t.Fatalf("lookup called %d times, want 0", lookupCalls.Load())
	}
}

func TestTokenIsCachedUntilNearExpiry(t *testing.T) {
	key := testKey(t)

	var mintCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mintCalls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      fmt.Sprintf("tok-%d", mintCalls.Load()),
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 42, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	now := time.Now()
	src.now = func() time.Time { return now }

	tok1, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() = %v", err)
	}
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if tok1 != tok2 {
		t.Errorf("Token() returned %q then %q, want the cached value reused", tok1, tok2)
	}
	if mintCalls.Load() != 1 {
		t.Errorf("mint called %d times, want 1 (second call should hit the cache)", mintCalls.Load())
	}
}

func TestTokenRefreshesBeforeExpiryMargin(t *testing.T) {
	key := testKey(t)

	var mintCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mintCalls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      fmt.Sprintf("tok-%d", mintCalls.Load()),
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 42, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	now := time.Now()
	src.now = func() time.Time { return now }

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if mintCalls.Load() != 1 {
		t.Fatalf("mint called %d times, want 1", mintCalls.Load())
	}

	// Jump to inside the 5 minute refresh margin before the hour-long token
	// actually expires: still "valid" per expires_at, but stale enough that a
	// dispatch could straddle the real expiry mid-request.
	now = now.Add(56 * time.Minute)

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if tok != "tok-2" {
		t.Errorf("Token() = %q, want a freshly minted token", tok)
	}
	if mintCalls.Load() != 2 {
		t.Errorf("mint called %d times, want 2 (refreshed inside the expiry margin)", mintCalls.Load())
	}
}

func TestTokenFailureIsAnError(t *testing.T) {
	key := testKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 42, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("Token() = nil, want an error for a 401 response")
	}
}

func TestTokenConcurrentCallsAreSafe(t *testing.T) {
	key := testKey(t)

	var mintCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			mintCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "tok",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src, err := New(srv.URL, "app-id", pkcs1PEM(key), 0, "acme/runner", srv.Client())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := src.Token(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Token() = %v", err)
	}
}

// verifyRS256 is the test-side mirror of the signing done in buildJWT: it
// re-derives the hash and checks the signature with the public key so the
// test does not just trust that some bytes were produced.
func verifyRS256(pub *rsa.PublicKey, signingInput string, sig []byte) error {
	hashed := sha256.Sum256([]byte(signingInput))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig)
}
