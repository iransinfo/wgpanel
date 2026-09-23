// Package redisclient wraps the Redis connection used for refresh-token storage
// (docs/PRD-security-access-control.md §6). Kept as a thin constructor rather than a
// bespoke abstraction - callers use *redis.Client directly.
package redisclient

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
