package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/KevinH126/publift-evidence-search/internal/domain"
)

// Redis wraps a Redis client for caching, rate limiting, and job queue.
type Redis struct {
	Client *redis.Client
}

// NewRedis creates a Redis connection.
func NewRedis(redisURL string) (*Redis, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Redis{Client: client}, nil
}

// ── Query Cache ──

const cacheTTL = 5 * time.Minute

func cacheKey(query string, topK int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", query, topK)))
	return "cache:" + hex.EncodeToString(h[:16])
}

// GetCachedSearch returns cached search results if they exist.
func (r *Redis) GetCachedSearch(ctx context.Context, query string, topK int) ([]domain.SearchResult, error) {
	key := cacheKey(query, topK)
	data, err := r.Client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, err
	}

	var results []domain.SearchResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// CacheSearch stores search results in Redis with TTL.
func (r *Redis) CacheSearch(ctx context.Context, query string, topK int, results []domain.SearchResult) error {
	key := cacheKey(query, topK)
	data, err := json.Marshal(results)
	if err != nil {
		return err
	}
	return r.Client.Set(ctx, key, data, cacheTTL).Err()
}

// ── Rate Limiting (Sliding Window) ──

const (
	rateLimitWindow = 1 * time.Minute
	rateLimitMax    = 60
)

// CheckRateLimit returns true if the request is allowed, false if rate limited.
func (r *Redis) CheckRateLimit(ctx context.Context, ip string) (bool, error) {
	key := "ratelimit:" + ip
	now := time.Now().UnixMilli()

	pipe := r.Client.Pipeline()

	// Remove old entries outside the window
	pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", now-int64(rateLimitWindow.Milliseconds())))
	// Add current request
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	// Count requests in window
	countCmd := pipe.ZCard(ctx, key)
	// Set expiry on the key
	pipe.Expire(ctx, key, rateLimitWindow)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return false, err
	}

	return countCmd.Val() <= rateLimitMax, nil
}

// ── Job Queue ──

const jobQueueKey = "jobs:pending"

// EnqueueJob pushes a study processing job to the Redis list.
func (r *Redis) EnqueueJob(ctx context.Context, job domain.Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return r.Client.LPush(ctx, jobQueueKey, data).Err()
}

// DequeueJob blocks until a job is available (or timeout), then returns it.
func (r *Redis) DequeueJob(ctx context.Context, timeout time.Duration) (*domain.Job, error) {
	result, err := r.Client.BRPop(ctx, timeout, jobQueueKey).Result()
	if err == redis.Nil {
		return nil, nil // timeout, no job
	}
	if err != nil {
		return nil, err
	}

	var job domain.Job
	if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Ping checks the Redis connection.
func (r *Redis) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}

func (r *Redis) Close() error {
	return r.Client.Close()
}
