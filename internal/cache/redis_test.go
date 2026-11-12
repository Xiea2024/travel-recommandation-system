package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"mini-recommender/internal/cache"
)

// miniredis 是纯内存的假 Redis：测试用它能覆盖真实协议往返，又不需要起容器。
func newTestCache(t *testing.T) (*cache.RedisCache, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	cli := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = cli.Close() })
	return cache.NewRedisCache(cli), mr
}

func TestGetMiss(t *testing.T) {
	c, _ := newTestCache(t)
	_, err := c.Get(context.Background(), "rec:v1:nope")
	// 未命中必须返回哨兵错误 ErrCacheMiss，而不是底层错误。
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("err = %v, want ErrCacheMiss", err)
	}
}

func TestSetGetRoundtrip(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	if err := c.Set(ctx, "rec:v1:my-001", []byte(`{"a":1}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "rec:v1:my-001")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %s", got)
	}
	if ttl := mr.TTL("rec:v1:my-001"); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("TTL = %v, want (0, 1m]", ttl)
	}
}

// TestRedisDownNotMiss 是降级逻辑的关键行为测试：
// Redis 挂了的时候 Get 必须返回普通错误而不是 ErrCacheMiss，
// API 层据此区分“缓存未命中”（回源并回填）和“缓存不可用”（只回源不回填失败）。
func TestRedisDownNotMiss(t *testing.T) {
	c, mr := newTestCache(t)
	mr.Close() // 模拟 Redis 宕机

	_, err := c.Get(context.Background(), "rec:v1:my-001")
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if errors.Is(err, cache.ErrCacheMiss) {
		t.Fatalf("Redis 故障绝不能报成缓存未命中: %v", err)
	}
}
