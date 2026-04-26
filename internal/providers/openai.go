package providers

import (
	"encoding/json"
	"net/http"
)

type OpenAIAdapter struct{}

type openAIPayload struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

func (a *OpenAIAdapter) DescribeRequest(r *http.Request, body []byte) (CacheDescriptor, error) {
	var payload openAIPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return CacheDescriptor{}, err
	}
	if len(payload.Messages) == 0 {
		return CacheDescriptor{Model: payload.Model, Stream: payload.Stream}, ErrUnsupportedProvider
	}

	var promptLines []string
	for _, msg := range payload.Messages {
		text := collectTextContent(msg.Content)
		if text == "" {
			continue
		}
		promptLines = append(promptLines, msg.Role+": "+text)
	}
	if len(promptLines) == 0 {
		return CacheDescriptor{Model: payload.Model, Stream: payload.Stream}, ErrUnsupportedProvider
	}

	digest, err := normalizeJSONBytes(body)
	if err != nil {
		return CacheDescriptor{}, err
	}

	return CacheDescriptor{
		Provider:      "openai",
		Model:         payload.Model,
		Stream:        payload.Stream,
		PromptText:    joinPromptLines(promptLines),
		RequestDigest: digest,
		Cacheable:     true,
	}, nil
}
