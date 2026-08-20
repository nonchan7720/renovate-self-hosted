package webhook_test

import (
	"errors"
	"testing"

	"github.com/nonchan7720/renovate-self-hosted/internal/webhook"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	const secret = "s3cret"
	body := []byte(`{"action":"edited"}`)
	valid := webhook.Sign(secret, body)

	tests := map[string]struct {
		header string
		body   []byte
		want   error
	}{
		"valid":            {header: valid, body: body},
		"missing":          {header: "", body: body, want: webhook.ErrMissingSignature},
		"wrong prefix":     {header: "sha1=" + valid[len("sha256="):], body: body, want: webhook.ErrInvalidSignature},
		"not hex":          {header: "sha256=zzzz", body: body, want: webhook.ErrInvalidSignature},
		"tampered body":    {header: valid, body: []byte(`{"action":"opened"}`), want: webhook.ErrInvalidSignature},
		"tampered digest":  {header: webhook.Sign("other", body), body: body, want: webhook.ErrInvalidSignature},
		"empty hex digest": {header: "sha256=", body: body, want: webhook.ErrInvalidSignature},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := webhook.VerifySignature(secret, tc.header, tc.body)
			if !errors.Is(err, tc.want) {
				t.Fatalf("VerifySignature() = %v, want %v", err, tc.want)
			}
		})
	}
}
