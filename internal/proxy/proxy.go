package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"llm-gateway/internal/config"
)

func NewHandler(cfg *config.Config) (http.Handler, error) {
	defaultTarget, err := url.Parse(cfg.DefaultUpstreamBaseURL)
	if err != nil || defaultTarget.Scheme == "" || defaultTarget.Host == "" {
		return nil, fmt.Errorf("invalid upstream base URL: %q", cfg.DefaultUpstreamBaseURL)
	}
	anthropicURL, err := url.Parse(cfg.AnthropicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Anthropic base URL: %w", err)
	}
	geminiURL, err := url.Parse(cfg.GeminiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Gemini base URL: %w", err)
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target, auth := routeRequest(pr.In.URL.Path, cfg, defaultTarget, anthropicURL, geminiURL)
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.SetXForwarded()

			for key, value := range auth {
				pr.Out.Header.Set(key, value)
			}
		},
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("upstream proxy error: %v", err)
			http.Error(w, "upstream LLM provider unavailable", http.StatusBadGateway)
		},
	}, nil
}

func routeRequest(path string, cfg *config.Config, defaultTarget, anthropicURL, geminiURL *url.URL) (*url.URL, map[string]string) {
	switch {
	case strings.Contains(path, "/v1/messages"):
		headers := map[string]string{}
		if cfg.AnthropicAPIKey != "" {
			headers["x-api-key"] = cfg.AnthropicAPIKey
			headers["anthropic-version"] = "2023-06-01"
		}
		return anthropicURL, headers
	case strings.Contains(path, ":generateContent"), strings.Contains(path, ":streamGenerateContent"):
		headers := map[string]string{}
		if cfg.GeminiAPIKey != "" {
			headers["x-goog-api-key"] = cfg.GeminiAPIKey
		}
		return geminiURL, headers
	default:
		headers := map[string]string{}
		if cfg.OpenAIAPIKey != "" {
			headers["Authorization"] = "Bearer " + cfg.OpenAIAPIKey
		}
		return defaultTarget, headers
	}
}

func NewHealthHandler(cacheConfigured, cacheHealthy bool) http.Handler {
	type statusResponse struct {
		Status string `json:"status"`
		Cache  struct {
			Configured bool `json:"configured"`
			Healthy    bool `json:"healthy"`
		} `json:"cache"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := statusResponse{Status: "ok"}
		resp.Cache.Configured = cacheConfigured
		resp.Cache.Healthy = cacheHealthy
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
