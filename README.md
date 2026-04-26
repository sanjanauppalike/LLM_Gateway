# LLM Gateway

`llm-gateway` is a Go gateway for OpenAI-compatible and adjacent LLM APIs. It applies Redis-backed rate limiting, can serve cached responses from Qdrant, and proxies requests to upstream model providers.

## What It Does

- Proxies `/v1/*` and `/v1beta/*` traffic
- Detects Anthropic and Gemini routes
- Enforces token-bucket rate limiting with Redis
- Optionally uses Qdrant + embeddings for semantic caching
- Exposes `/healthz` and `/readyz`
- Ships with Docker and Helm packaging

## Repository Layout

```text
.
├── cmd/gateway/
├── internal/cache/
├── internal/config/
├── internal/providers/
├── internal/proxy/
├── internal/ratelimit/
├── internal/telemetry/
├── deploy/charts/llm-gateway/
├── .env.example
├── Dockerfile
└── go.mod
```

## Request Flow

1. Request arrives at `/v1/*` or `/v1beta/*`
2. Redis rate limiter checks `Authorization: Bearer <token>`
3. If cache is enabled, the request is normalized and looked up in Qdrant
4. Cache hits are served directly
5. Cache misses are proxied upstream
6. Successful responses are asynchronously queued for cache storage

## Quick Start

Use [.env.example](/Users/sanj/Documents/Github-RateLimitter-LLM/.env.example) as your starting point.

For the fastest first run, disable cache:

```bash
CACHE_ENABLED=false
```

Build:

```bash
docker build -t llm-gateway:local .
```

Start Redis:

```bash
docker run --rm --name llm-gateway-redis -p 6379:6379 redis:7-alpine
```

Run the gateway:

```bash
docker run --rm -p 8080:8080 \
  -e REDIS_HOST=host.docker.internal \
  -e REDIS_PORT=6379 \
  -e CACHE_ENABLED=false \
  -e UPSTREAM_BASE_URL=http://host.docker.internal:11434 \
  llm-gateway:local
```

Health check:

```bash
curl -i http://localhost:8080/healthz
```

Example request:

```bash
curl -sS http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer dev-user-token" \
  -d '{
    "model":"gpt-4.1-mini",
    "messages":[
      {"role":"user","content":"Explain semantic caching in one paragraph."}
    ]
  }'
```

## Core Environment Variables

| Variable | Purpose | Default |
| --- | --- | --- |
| `PORT` | HTTP listen port | `8080` |
| `UPSTREAM_BASE_URL` | Default OpenAI-compatible upstream | `http://localhost:11434` |
| `OPENAI_API_KEY` | Auth header for default upstream requests | empty |
| `ANTHROPIC_API_KEY` | Auth header for Anthropic routes | empty |
| `GEMINI_API_KEY` | Auth header for Gemini routes | empty |
| `REDIS_HOST` | Redis host | `localhost` |
| `REDIS_PORT` | Redis port | `6379` |
| `RATE_LIMIT_MAX_REQUESTS` | Bucket size per token | `5` |
| `RATE_LIMIT_WINDOW` | Refill window | `1m` |
| `RATE_LIMIT_FAIL_OPEN` | Allow traffic if Redis is down | `true` |
| `CACHE_ENABLED` | Enable semantic cache | `true` |
| `CACHE_FAIL_OPEN` | Continue startup if cache fails | `true` |
| `QDRANT_HOST` | Qdrant host | `localhost` |
| `QDRANT_PORT` | Qdrant gRPC port | `6334` |
| `EMBEDDING_URL` | Embedding endpoint | `http://localhost:11434/api/embeddings` |
| `EMBEDDING_MODEL` | Embedding model | `all-minilm` |
| `TELEMETRY_EXPORTER` | `stdout` or `otlp` | `stdout` |

## Routing

- `"/v1/messages"` routes to Anthropic
- `":generateContent"` and `":streamGenerateContent"` route to Gemini
- Everything else goes to `UPSTREAM_BASE_URL`

## Cache Notes

The cache is strict by default. A cached response is served only when these align:

- provider
- model
- stream mode
- normalized request digest
- similarity threshold
- TTL

This is designed to avoid unsafe cache hits across different conversation histories.

## Health Endpoints

- `GET /healthz`
- `GET /readyz`

Example response:

```json
{"status":"ok","cache":{"configured":false,"healthy":true}}
```

## Build And Test

Docker build runs both tests and compile:

```bash
docker build -t llm-gateway:local .
```

If Go is installed locally:

```bash
go test ./...
go build -o llm-gateway ./cmd/gateway
```

## Helm

The chart lives in [deploy/charts/llm-gateway](/Users/sanj/Documents/Github-RateLimitter-LLM/deploy/charts/llm-gateway).

Install:

```bash
cd deploy/charts/llm-gateway
helm upgrade --install llm-gateway . --namespace llm-gateway --create-namespace
```

The chart:

- maps `values.yaml` to runtime env vars
- stores provider keys in a Kubernetes `Secret`
- uses `/readyz` and `/healthz` for probes

## Troubleshooting

`401 missing or invalid Authorization header`

- Send `Authorization: Bearer <token>`

`429 rate limit exceeded`

- Increase `RATE_LIMIT_MAX_REQUESTS` or `RATE_LIMIT_WINDOW`

Gateway starts but upstream requests fail

- Check `UPSTREAM_BASE_URL` and provider API keys

Cache does not start

- Verify `QDRANT_HOST`, `QDRANT_PORT`, and `EMBEDDING_URL`
- Set `CACHE_ENABLED=false` to isolate proxy behavior

Low cache hit rate

- Compare full request bodies, not just final prompt text
- Confirm model names match
- Review `REQUIRE_EXACT_REQUEST_DIGEST` and similarity settings

## Current Limitations

- End-to-end cache validation still requires a real Qdrant instance and embedding service
- `/readyz` currently shares the same handler as `/healthz`
- The current tests are targeted, not exhaustive
