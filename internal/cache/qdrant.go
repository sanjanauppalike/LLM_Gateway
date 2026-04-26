package cache

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"llm-gateway/internal/config"
	"llm-gateway/internal/providers"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const CollectionName = "gateway_prompt_cache"

type ClientOptions struct {
	Enabled                   bool
	SaveQueueSize             int
	SaveWorkers               int
	SaveTimeout               time.Duration
	VectorSize                int
	MaxCacheableBodyBytes     int64
	MaxCacheableResponseBytes int64
	CacheEntryTTL             time.Duration
	SearchLimit               int
	CleanupInterval           time.Duration
	CleanupTimeout            time.Duration
	CleanupEnabled            bool
	IndexPayloadFields        bool
	StrictSemanticCacheMatch  bool
	MinimumSemanticSimilarity float32
	RequireExactRequestDigest bool
}

type saveJob struct {
	vector     []float32
	response   []byte
	descriptor providers.CacheDescriptor
}

type QdrantClient struct {
	client                    *qdrant.Client
	enabled                   bool
	saveQueue                 chan saveJob
	saveTimeout               time.Duration
	maxCacheableBodyBytes     int64
	maxCacheableResponseBytes int64
	cacheEntryTTL             time.Duration
	searchLimit               int
	cleanupInterval           time.Duration
	cleanupTimeout            time.Duration
	cleanupEnabled            bool
	indexPayloadFields        bool
	strictSemanticMatch       bool
	minimumSimilarity         float32
	requireExactRequestDigest bool
	cleanupStop               chan struct{}
	searchOverride            func(context.Context, []float32, providers.CacheDescriptor) (bool, []byte)
	enqueueOverride           func([]float32, []byte, providers.CacheDescriptor) bool
	cleanupRunsCounter        metric.Int64Counter
	cleanupDeletedCounter     metric.Int64Counter
	cleanupErrorCounter       metric.Int64Counter
	cleanupDurationHistogram  metric.Float64Histogram
	wg                        sync.WaitGroup
	mu                        sync.RWMutex
	isClosed                  bool
}

func NewQdrantClient(host string, port int, opts ClientOptions) (*QdrantClient, error) {
	if !opts.Enabled {
		return nil, nil
	}
	if opts.SaveQueueSize <= 0 {
		opts.SaveQueueSize = config.DefaultCacheSaveQueueSize
	}
	if opts.SaveWorkers <= 0 {
		opts.SaveWorkers = config.DefaultCacheSaveWorkers
	}
	if opts.SaveTimeout <= 0 {
		opts.SaveTimeout = config.DefaultCacheSaveTimeout
	}
	if opts.MaxCacheableBodyBytes <= 0 {
		opts.MaxCacheableBodyBytes = config.DefaultMaxCacheableBody
	}
	if opts.MaxCacheableResponseBytes <= 0 {
		opts.MaxCacheableResponseBytes = config.DefaultMaxCacheableResponse
	}
	if opts.CacheEntryTTL <= 0 {
		opts.CacheEntryTTL = config.DefaultCacheEntryTTL
	}
	if opts.SearchLimit <= 0 {
		opts.SearchLimit = config.DefaultCacheSearchLimit
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = config.DefaultCacheCleanupInterval
	}
	if opts.CleanupTimeout <= 0 {
		opts.CleanupTimeout = config.DefaultCacheCleanupTimeout
	}
	if opts.MinimumSemanticSimilarity <= 0 {
		opts.MinimumSemanticSimilarity = config.DefaultMinimumSimilarity
	}

	vectorSize := opts.VectorSize
	if vectorSize <= 0 {
		ctx, cancel := context.WithTimeout(context.Background(), embeddingConfig.Timeout)
		detectedSize, err := GetEmbeddingVectorSize(ctx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to detect embedding vector size: %w", err)
		}
		vectorSize = detectedSize
	}

	client, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: port})
	if err != nil {
		return nil, err
	}

	qc := &QdrantClient{
		client:                    client,
		enabled:                   true,
		saveQueue:                 make(chan saveJob, opts.SaveQueueSize),
		saveTimeout:               opts.SaveTimeout,
		maxCacheableBodyBytes:     opts.MaxCacheableBodyBytes,
		maxCacheableResponseBytes: opts.MaxCacheableResponseBytes,
		cacheEntryTTL:             opts.CacheEntryTTL,
		searchLimit:               opts.SearchLimit,
		cleanupInterval:           opts.CleanupInterval,
		cleanupTimeout:            opts.CleanupTimeout,
		cleanupEnabled:            opts.CleanupEnabled,
		indexPayloadFields:        opts.IndexPayloadFields,
		strictSemanticMatch:       opts.StrictSemanticCacheMatch,
		minimumSimilarity:         opts.MinimumSemanticSimilarity,
		requireExactRequestDigest: opts.RequireExactRequestDigest,
		cleanupStop:               make(chan struct{}),
	}
	qc.initCleanupMetrics()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := client.CollectionExists(ctx, CollectionName)
	if err != nil {
		return nil, err
	}
	if !exists {
		err = client.CreateCollection(ctx, &qdrant.CreateCollection{
			CollectionName: CollectionName,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     uint64(vectorSize),
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return nil, err
		}
	}
	if qc.indexPayloadFields {
		if err := qc.ensurePayloadIndexes(ctx); err != nil {
			log.Printf("payload index creation failed: %v", err)
		}
	}

	qc.startSaveWorkers(opts.SaveWorkers)
	if qc.cleanupEnabled {
		qc.startCleanupWorker()
	}
	return qc, nil
}

func (qc *QdrantClient) initCleanupMetrics() {
	meter := otel.Meter("llm-gateway.cache.qdrant")
	if counter, err := meter.Int64Counter("gateway_cache_cleanup_runs_total"); err == nil {
		qc.cleanupRunsCounter = counter
	}
	if counter, err := meter.Int64Counter("gateway_cache_cleanup_deleted_total"); err == nil {
		qc.cleanupDeletedCounter = counter
	}
	if counter, err := meter.Int64Counter("gateway_cache_cleanup_errors_total"); err == nil {
		qc.cleanupErrorCounter = counter
	}
	if hist, err := meter.Float64Histogram("gateway_cache_cleanup_duration_seconds"); err == nil {
		qc.cleanupDurationHistogram = hist
	}
}

func (qc *QdrantClient) startCleanupWorker() {
	qc.wg.Add(1)
	go func() {
		defer qc.wg.Done()
		ticker := time.NewTicker(qc.cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-qc.cleanupStop:
				return
			case <-ticker.C:
				started := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), qc.cleanupTimeout)
				deleted, err := qc.cleanupExpiredEntries(ctx)
				cancel()
				qc.recordCleanupRunMetrics(context.Background(), deleted, time.Since(started), err)
			}
		}
	}()
}

func (qc *QdrantClient) recordCleanupRunMetrics(ctx context.Context, deleted int64, duration time.Duration, runErr error) {
	if qc.cleanupRunsCounter != nil {
		qc.cleanupRunsCounter.Add(ctx, 1)
	}
	if deleted > 0 && qc.cleanupDeletedCounter != nil {
		qc.cleanupDeletedCounter.Add(ctx, deleted)
	}
	if runErr != nil && qc.cleanupErrorCounter != nil {
		qc.cleanupErrorCounter.Add(ctx, 1)
	}
	if qc.cleanupDurationHistogram != nil {
		qc.cleanupDurationHistogram.Record(ctx, duration.Seconds())
	}
}

func (qc *QdrantClient) cleanupExpiredEntries(ctx context.Context) (int64, error) {
	now := float64(time.Now().Unix())
	exact := true
	count, err := qc.client.Count(ctx, &qdrant.CountPoints{
		CollectionName: CollectionName,
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewRange("expires_at_unix", &qdrant.Range{Lte: &now}),
			},
		},
		Exact: &exact,
	})
	if err != nil || count == 0 {
		return int64(count), err
	}
	wait := false
	_, err = qc.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: CollectionName,
		Wait:           &wait,
		Points: qdrant.NewPointsSelectorFilter(&qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewRange("expires_at_unix", &qdrant.Range{Lte: &now}),
			},
		}),
	})
	return int64(count), err
}

func (qc *QdrantClient) ensurePayloadIndexes(ctx context.Context) error {
	indexes := []struct {
		name string
		kind qdrant.FieldType
	}{
		{name: "provider", kind: qdrant.FieldType_FieldTypeKeyword},
		{name: "model", kind: qdrant.FieldType_FieldTypeKeyword},
		{name: "request_digest", kind: qdrant.FieldType_FieldTypeKeyword},
		{name: "expires_at_unix", kind: qdrant.FieldType_FieldTypeInteger},
	}
	for _, index := range indexes {
		if err := qc.createFieldIndex(ctx, index.name, index.kind); err != nil {
			return err
		}
	}
	return nil
}

func (qc *QdrantClient) createFieldIndex(ctx context.Context, fieldName string, fieldType qdrant.FieldType) error {
	wait := true
	_, err := qc.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
		CollectionName: CollectionName,
		Wait:           &wait,
		FieldName:      fieldName,
		FieldType:      &fieldType,
	})
	if err == nil || isIgnorableIndexError(err) {
		return nil
	}
	return err
}

func isIgnorableIndexError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already exists") || strings.Contains(message, "duplicate")
}

func (qc *QdrantClient) startSaveWorkers(workers int) {
	for i := 0; i < workers; i++ {
		qc.wg.Add(1)
		go qc.batchWorker()
	}
}

func (qc *QdrantClient) batchWorker() {
	defer qc.wg.Done()

	const maxBatchSize = 10
	const flushInterval = 500 * time.Millisecond

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	batch := make([]*qdrant.PointStruct, 0, maxBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), qc.saveTimeout)
		defer cancel()
		wait := false
		_, err := qc.client.Upsert(ctx, &qdrant.UpsertPoints{
			CollectionName: CollectionName,
			Wait:           &wait,
			Points:         batch,
		})
		if err != nil {
			log.Printf("Qdrant batch upsert failed: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case job, ok := <-qc.saveQueue:
			if !ok {
				flush()
				return
			}
			nowUnix := time.Now().Unix()
			expiresAt := time.Now().Add(qc.cacheEntryTTL).Unix()
			batch = append(batch, &qdrant.PointStruct{
				Id:      qdrant.NewIDUUID(uuid.New().String()),
				Vectors: qdrant.NewVectors(job.vector...),
				Payload: qdrant.NewValueMap(map[string]any{
					"provider":        job.descriptor.Provider,
					"model":           job.descriptor.Model,
					"stream":          job.descriptor.Stream,
					"request_digest":  job.descriptor.RequestDigest,
					"response":        string(job.response),
					"created_at_unix": nowUnix,
					"expires_at_unix": expiresAt,
				}),
			})
			if len(batch) >= maxBatchSize {
				flush()
				ticker.Reset(flushInterval)
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (qc *QdrantClient) search(ctx context.Context, vector []float32, descriptor providers.CacheDescriptor) (bool, []byte) {
	if qc.searchOverride != nil {
		return qc.searchOverride(ctx, vector, descriptor)
	}
	return qc.Search(ctx, vector, descriptor)
}

func (qc *QdrantClient) enqueue(vector []float32, response []byte, descriptor providers.CacheDescriptor) bool {
	if qc.enqueueOverride != nil {
		return qc.enqueueOverride(vector, response, descriptor)
	}
	return qc.EnqueueSaveWithMetadata(vector, response, descriptor)
}

func (qc *QdrantClient) EnqueueSaveWithMetadata(vector []float32, response []byte, descriptor providers.CacheDescriptor) bool {
	qc.mu.RLock()
	defer qc.mu.RUnlock()
	if qc.isClosed {
		return false
	}
	job := saveJob{vector: vector, response: response, descriptor: descriptor}
	select {
	case qc.saveQueue <- job:
		return true
	default:
		return false
	}
}

func (qc *QdrantClient) Search(ctx context.Context, vector []float32, descriptor providers.CacheDescriptor) (bool, []byte) {
	limit := uint64(qc.searchLimit)
	threshold := qc.minimumSimilarity
	results, err := qc.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: CollectionName,
		Query:          qdrant.NewQuery(vector...),
		Limit:          &limit,
		WithPayload:    qdrant.NewWithPayload(true),
		ScoreThreshold: &threshold,
	})
	if err != nil || len(results) == 0 {
		return false, nil
	}

	for _, hit := range results {
		if hit.Payload == nil {
			continue
		}
		if hit.Payload["provider"].GetStringValue() != descriptor.Provider {
			continue
		}
		if hit.Payload["model"].GetStringValue() != descriptor.Model {
			continue
		}
		if hit.Payload["stream"].GetBoolValue() != descriptor.Stream {
			continue
		}
		if expiresAt := hit.Payload["expires_at_unix"].GetIntegerValue(); expiresAt > 0 && expiresAt <= time.Now().Unix() {
			continue
		}
		if qc.requireExactRequestDigest && hit.Payload["request_digest"].GetStringValue() != descriptor.RequestDigest {
			continue
		}
		if qc.strictSemanticMatch && hit.Score < qc.minimumSimilarity {
			continue
		}
		response := hit.Payload["response"].GetStringValue()
		if response == "" {
			continue
		}
		return true, []byte(response)
	}

	return false, nil
}

func (qc *QdrantClient) Close() {
	if qc == nil {
		return
	}
	if qc.cleanupEnabled {
		close(qc.cleanupStop)
	}
	qc.mu.Lock()
	qc.isClosed = true
	close(qc.saveQueue)
	qc.mu.Unlock()
	qc.wg.Wait()
	qc.client.Close()
}
