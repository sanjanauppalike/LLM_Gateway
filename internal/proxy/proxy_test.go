package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"llm-gateway/internal/config"
)

func TestHealthHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	NewHealthHandler(true, false).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestRouteRequestChoosesGeminiForV1Beta(t *testing.T) {
	cfg := &config.Config{GeminiAPIKey: "abc"}
	defaultTarget, _ := url.Parse("http://default")
	anthropicURL, _ := url.Parse("https://api.anthropic.com")
	geminiURL, _ := url.Parse("https://generativelanguage.googleapis.com")

	target, headers := routeRequest("/v1beta/models/gemini-2.5-flash:generateContent", cfg, defaultTarget, anthropicURL, geminiURL)
	if target.Host != geminiURL.Host {
		t.Fatalf("expected gemini host, got %s", target.Host)
	}
	if headers["x-goog-api-key"] != "abc" {
		t.Fatalf("expected Gemini API key header")
	}
}
