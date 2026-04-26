package providers

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIAdapterUsesFullConversationDigestAndPrompt(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4.1-mini",
		"messages":[
			{"role":"system","content":"be terse"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"hi"},
			{"role":"user","content":"what is caching?"}
		]
	}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))

	desc, err := (&OpenAIAdapter{}).DescribeRequest(req, body)
	if err != nil {
		t.Fatalf("DescribeRequest returned error: %v", err)
	}
	if !strings.Contains(desc.PromptText, "system: be terse") || !strings.Contains(desc.PromptText, "user: hello") {
		t.Fatalf("expected full conversation in prompt text, got %q", desc.PromptText)
	}
	if desc.RequestDigest == "" {
		t.Fatalf("expected request digest to be set")
	}
}

func TestGeminiAdapterParsesModelFromV1BetaPath(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(string(body)))

	desc, err := (&GeminiAdapter{}).DescribeRequest(req, body)
	if err != nil {
		t.Fatalf("DescribeRequest returned error: %v", err)
	}
	if desc.Model != "gemini-2.5-flash" {
		t.Fatalf("unexpected model %q", desc.Model)
	}
}
