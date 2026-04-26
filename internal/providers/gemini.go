package providers

import (
	"encoding/json"
	"net/http"
	"strings"
)

type GeminiAdapter struct{}

type geminiPayload struct {
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"contents"`
	SystemInstruction struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"systemInstruction"`
}

func (a *GeminiAdapter) DescribeRequest(r *http.Request, body []byte) (CacheDescriptor, error) {
	var payload geminiPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return CacheDescriptor{}, err
	}

	model := parseGeminiModelFromPath(r.URL.Path)
	stream := strings.Contains(r.URL.Path, "streamGenerateContent")

	var promptLines []string
	if len(payload.SystemInstruction.Parts) > 0 {
		var sysParts []string
		for _, part := range payload.SystemInstruction.Parts {
			if part.Text != "" {
				sysParts = append(sysParts, part.Text)
			}
		}
		if len(sysParts) > 0 {
			promptLines = append(promptLines, "system: "+strings.Join(sysParts, "\n"))
		}
	}
	for _, content := range payload.Contents {
		var parts []string
		for _, part := range content.Parts {
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) == 0 {
			continue
		}
		role := content.Role
		if role == "" {
			role = "user"
		}
		promptLines = append(promptLines, role+": "+strings.Join(parts, "\n"))
	}
	if len(promptLines) == 0 {
		return CacheDescriptor{Model: model, Stream: stream}, ErrUnsupportedProvider
	}

	digest, err := normalizeJSONBytes(body)
	if err != nil {
		return CacheDescriptor{}, err
	}

	return CacheDescriptor{
		Provider:      "gemini",
		Model:         model,
		Stream:        stream,
		PromptText:    joinPromptLines(promptLines),
		RequestDigest: digest,
		Cacheable:     true,
	}, nil
}

func parseGeminiModelFromPath(path string) string {
	for _, segment := range strings.Split(path, "/") {
		if strings.HasPrefix(segment, "models/") {
			return strings.Split(strings.TrimPrefix(segment, "models/"), ":")[0]
		}
		if strings.Contains(segment, ":") && !strings.HasPrefix(segment, "v1") && !strings.HasPrefix(segment, "v1beta") {
			return strings.Split(segment, ":")[0]
		}
	}
	return "unknown-gemini-model"
}
