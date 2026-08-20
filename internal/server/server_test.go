package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/server"
)

func TestRoutes(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Addr: ":0", Path: "/hooks/github"}
	webhook := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	srv := server.New(cfg, webhook, slog.New(slog.NewTextHandler(io.Discard, nil)))

	tests := map[string]struct {
		method, path string
		want         int
	}{
		"webhook":        {http.MethodPost, "/hooks/github", http.StatusAccepted},
		"healthz":        {http.MethodGet, "/healthz", http.StatusOK},
		"readyz":         {http.MethodGet, "/readyz", http.StatusOK},
		"unknown path":   {http.MethodGet, "/nope", http.StatusNotFound},
		"default path":   {http.MethodPost, "/webhook", http.StatusNotFound},
		"healthz method": {http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}
