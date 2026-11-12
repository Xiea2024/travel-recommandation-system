// Package cache 封装推荐结果的 Redis 缓存。
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrCacheMiss 是“缓存里没有这个 key”的哨兵错误，属于正常业务路径。
// 它必须和 Redis 连接失败严格区分：只有后者才触发降级回源。
var ErrCacheMiss = errors.New("cache miss")

// Cache 是 API 层依赖的抽象接口，测试时用内存 fake 替换真实 Redis。
type Cache interface {
	// Get 命中返回数据；未命中返回 ErrCacheMiss；Redis 故障返回其他错误。
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
}

// RedisCache 用 go-redis 客户端实现 Cache。
type RedisCache struct {
	cli *redis.Client
}

func NewRedisCache(cli *redis.Client) *RedisCache {
	return &RedisCache{cli: cli}
}

func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.cli.Get(ctx, key).Result()
	// go-redis 用 redis.Nil 表示 key 不存在；把它映射成自己的哨兵错误 ErrCacheMiss，
	// 业务层只认这一种“未命中”，不用关心客户端细节。
	if errors.Is(err, redis.Nil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		// %w 包装错误：错误信息带上 key 方便排查，同时保留原始错误，
		// 调用方用 errors.Is(err, context.DeadlineExceeded) 之类仍能识别出具体是哪种失败。
		return nil, fmt.Errorf("redis get %q: %w", key, err)
	}
	return []byte(val), nil
}

func (c *RedisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := c.cli.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}
