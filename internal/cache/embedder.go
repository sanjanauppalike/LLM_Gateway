package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type EmbedderConfig struct {
	URL     string
	Model   string
	Timeout time.Duration
}

var embeddingConfig EmbedderConfig
var embeddingHTTPClient *http.Client

func ConfigureEmbedder(cfg EmbedderConfig) {
	embeddingConfig = cfg
	embeddingHTTPClient = &http.Client{Timeout: cfg.Timeout}
}

type ollamaEmbeddingReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbeddingResp struct {
	Embedding []float32 `json:"embedding"`
}

func GetEmbedding(ctx context.Context, prompt string) ([]float32, error) {
	if embeddingHTTPClient == nil || embeddingConfig.URL == "" || embeddingConfig.Model == "" || embeddingConfig.Timeout <= 0 {
		return nil, fmt.Errorf("embedder is not configured")
	}

	body, err := json.Marshal(ollamaEmbeddingReq{
		Model:  embeddingConfig.Model,
		Prompt: prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embeddingConfig.URL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := embeddingHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call embedding API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API returned status: %d", resp.StatusCode)
	}

	var parsed ollamaEmbeddingResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to decode embedding response: %w", err)
	}
	if len(parsed.Embedding) == 0 {
		return nil, fmt.Errorf("embedding API returned an empty vector")
	}
	return parsed.Embedding, nil
}

func GetEmbeddingVectorSize(ctx context.Context) (int, error) {
	embedding, err := GetEmbedding(ctx, "vector-size-probe")
	if err != nil {
		return 0, err
	}
	return len(embedding), nil
}
