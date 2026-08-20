// Package webhook receives GitHub webhook deliveries and turns the ones that
// concern Renovate into dispatch jobs.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// SignatureHeader is the header GitHub uses for the HMAC-SHA256 signature.
const SignatureHeader = "X-Hub-Signature-256"

const signaturePrefix = "sha256="

// Errors returned by VerifySignature.
var (
	ErrMissingSignature = errors.New("missing signature header")
	ErrInvalidSignature = errors.New("signature does not match payload")
)

// VerifySignature checks the X-Hub-Signature-256 header against the raw request
// body. The comparison is constant time.
func VerifySignature(secret string, header string, body []byte) error {
	if header == "" {
		return ErrMissingSignature
	}
	hexDigest, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return ErrInvalidSignature
	}
	got, err := hex.DecodeString(hexDigest)
	if err != nil {
		return ErrInvalidSignature
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

// Sign returns the signature header value for body. It exists so tests and
// operators can reproduce what GitHub sends.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
