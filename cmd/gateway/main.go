package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm-gateway/internal/cache"
	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/ratelimit"
	"llm-gateway/internal/telemetry"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	cfg := config.Load()

	shutdownTelemetry, err := telemetry.InitProvider(telemetry.Options{
		Exporter:         cfg.TelemetryExporter,
		OTLPEndpoint:     cfg.TelemetryOTLPEndpoint,
		OTLPInsecure:     cfg.TelemetryOTLPInsecure,
		MetricInterval:   cfg.TelemetryMetricInterval,
		TraceSampleRatio: cfg.TelemetryTraceSampleRatio,
		ServiceName:      cfg.ServiceName,
		ServiceVersion:   cfg.ServiceVersion,
	})
	if err != nil {
		log.Fatalf("failed to initialize telemetry: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(ctx)
	}()

	cache.ConfigureEmbedder(cache.EmbedderConfig{
		URL:     cfg.EmbeddingURL,
		Model:   cfg.EmbeddingModel,
		Timeout: cfg.EmbeddingTimeout,
	})

	qClient, cacheErr := cache.NewQdrantClient(cfg.QdrantHost, cfg.QdrantPort, cache.ClientOptions{
		Enabled:                    cfg.CacheEnabled,
		SaveQueueSize:              cfg.CacheSaveQueueSize,
		SaveWorkers:                cfg.CacheSaveWorkers,
		SaveTimeout:                cfg.CacheSaveTimeout,
		VectorSize:                 cfg.CacheVectorSize,
		CacheEntryTTL:              cfg.CacheEntryTTL,
		SearchLimit:                cfg.CacheSearchLimit,
		CleanupInterval:            cfg.CacheCleanupInterval,
		CleanupTimeout:             cfg.CacheCleanupTimeout,
		CleanupEnabled:             cfg.CacheCleanupEnabled,
		IndexPayloadFields:         cfg.CacheIndexPayloadFields,
		MaxCacheableBodyBytes:      cfg.MaxCacheableBodyBytes,
		MaxCacheableResponseBytes:  cfg.MaxCacheableResponseBytes,
		StrictSemanticCacheMatch:   cfg.StrictSemanticCacheMatch,
		MinimumSemanticSimilarity:  cfg.MinimumSemanticSimilarity,
		RequireExactRequestDigest:  cfg.RequireExactRequestDigest,
	})
	if cacheErr != nil {
		if cfg.CacheFailOpen {
			log.Printf("cache disabled after startup failure: %v", cacheErr)
		} else {
			log.Fatalf("failed to initialize cache: %v", cacheErr)
		}
	}
	if qClient != nil {
		defer qClient.Close()
	}

	rateLimiter, err := ratelimit.NewLimiterWithOptions(cfg.RedisHost, cfg.RedisPort, ratelimit.Options{
		MaxRequests: cfg.RateLimitMaxRequests,
		Window:      cfg.RateLimitWindow,
		FailOpen:    cfg.RateLimitFailOpen,
	})
	if err != nil {
		log.Fatalf("failed to initialize rate limiter: %v", err)
	}
	defer rateLimiter.Close()

	llmProxy, err := proxy.NewHandler(cfg)
	if err != nil {
		log.Fatalf("failed to initialize proxy: %v", err)
	}

	handler := http.Handler(llmProxy)
	if qClient != nil {
		handler = qClient.Middleware(handler)
	}
	handler = rateLimiter.Middleware(handler)
	handler = otelhttp.NewHandler(handler, "llm_gateway_request")

	mux := http.NewServeMux()
	healthHandler := proxy.NewHealthHandler(qClient != nil, cacheErr == nil)
	mux.Handle("/healthz", healthHandler)
	mux.Handle("/readyz", healthHandler)
	mux.Handle("/v1/", handler)
	mux.Handle("/v1beta/", handler)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      180 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Printf("gateway listening on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
