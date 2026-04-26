package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

var ErrUnsupportedProvider = errors.New("unsupported provider format")

type CacheDescriptor struct {
	Provider      string
	Model         string
	Stream        bool
	PromptText    string
	RequestDigest string
	Cacheable     bool
}

type LLMProvider interface {
	DescribeRequest(r *http.Request, body []byte) (CacheDescriptor, error)
}

func DetectProvider(r *http.Request) LLMProvider {
	if strings.Contains(r.URL.Path, "/v1/messages") {
		return &AnthropicAdapter{}
	}
	if strings.Contains(r.URL.Path, ":generateContent") || strings.Contains(r.URL.Path, ":streamGenerateContent") {
		return &GeminiAdapter{}
	}
	return &OpenAIAdapter{}
}

func HashNormalizedJSON(v any) (string, error) {
	normalized, err := normalizeJSONValue(v)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

func normalizeJSONBytes(body []byte) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	return HashNormalizedJSON(value)
}

func normalizeJSONValue(v any) (any, error) {
	switch typed := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(typed))
		for _, key := range keys {
			normalized, err := normalizeJSONValue(typed[key])
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out = append(out, normalized)
		}
		return out, nil
	default:
		return typed, nil
	}
}

func collectTextContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var parts []string
		for _, item := range typed {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if itemMap["type"] == "text" {
				if text, ok := itemMap["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
