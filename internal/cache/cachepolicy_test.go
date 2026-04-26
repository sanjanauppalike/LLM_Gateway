package cache

import (
	"context"
	"testing"

	"llm-gateway/internal/providers"
)

func TestSearchOverrideAllowsPolicyTesting(t *testing.T) {
	qc := &QdrantClient{}
	expected := providers.CacheDescriptor{Provider: "openai", Model: "gpt-4.1-mini", RequestDigest: "abc"}
	qc.searchOverride = func(_ context.Context, _ []float32, descriptor providers.CacheDescriptor) (bool, []byte) {
		if descriptor != expected {
			t.Fatalf("unexpected descriptor: %#v", descriptor)
		}
		return true, []byte(`{"cached":true}`)
	}
	hit, body := qc.search(context.Background(), []float32{1}, expected)
	if !hit || string(body) != `{"cached":true}` {
		t.Fatalf("expected cache hit")
	}
}
