package cache

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"llm-gateway/internal/providers"
)

var getEmbedding = GetEmbedding

type responseCapturer struct {
	http.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	maxBytes   int64
	overLimit  bool
}

func (rc *responseCapturer) Write(b []byte) (int, error) {
	if !rc.overLimit {
		if rc.maxBytes <= 0 || int64(rc.body.Len()+len(b)) <= rc.maxBytes {
			_, _ = rc.body.Write(b)
		} else {
			rc.overLimit = true
		}
	}
	return rc.ResponseWriter.Write(b)
}

func (rc *responseCapturer) WriteHeader(statusCode int) {
	rc.statusCode = statusCode
	rc.ResponseWriter.WriteHeader(statusCode)
}

func (rc *responseCapturer) Flush() {
	if flusher, ok := rc.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (qc *QdrantClient) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !qc.enabled {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > qc.maxCacheableBodyBytes && qc.maxCacheableBodyBytes > 0 {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		limitedBody := io.Reader(r.Body)
		if qc.maxCacheableBodyBytes > 0 {
			limitedBody = io.LimitReader(r.Body, qc.maxCacheableBodyBytes+1)
		}
		bodyBytes, err := io.ReadAll(limitedBody)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusInternalServerError)
			return
		}
		if qc.maxCacheableBodyBytes > 0 && int64(len(bodyBytes)) > qc.maxCacheableBodyBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		descriptor, err := providers.DetectProvider(r).DescribeRequest(r, bodyBytes)
		if err != nil || !descriptor.Cacheable || descriptor.PromptText == "" || descriptor.Model == "" {
			next.ServeHTTP(w, r)
			return
		}

		vector, err := getEmbedding(r.Context(), descriptor.PromptText)
		if err != nil {
			log.Printf("embedding generation failed: %v", err)
			next.ServeHTTP(w, r)
			return
		}

		if hit, cached := qc.search(r.Context(), vector, descriptor); hit {
			if descriptor.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				scanner := bufio.NewScanner(bytes.NewReader(cached))
				for scanner.Scan() {
					_, _ = w.Write(scanner.Bytes())
					_, _ = w.Write([]byte("\n"))
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					time.Sleep(1 * time.Millisecond)
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(cached)
			return
		}

		capturer := &responseCapturer{
			ResponseWriter: w,
			body:           &bytes.Buffer{},
			statusCode:     http.StatusOK,
			maxBytes:       qc.maxCacheableResponseBytes,
		}
		next.ServeHTTP(capturer, r)

		if r.Context().Err() != nil {
			return
		}
		if capturer.statusCode == http.StatusOK && !capturer.overLimit {
			if !qc.enqueue(vector, capturer.body.Bytes(), descriptor) {
				log.Printf("cache save queue full; dropping cache write")
			}
		}
	})
}
