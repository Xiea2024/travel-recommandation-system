// Package cache 除了推荐结果缓存，还提供埋点事件日志（Redis 实现）。
// 不引入数据库：事件放 Redis 列表，LPUSH+LTRIM 控制长度。
package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// EventAppender 抽象埋点日志写入，测试时用内存 fake 替换 Redis。
type EventAppender interface {
	Append(ctx context.Context, line []byte) error
}

// EventLog 把埋点事件追加到 Redis 列表（训练数据飞轮的数据源）。
type EventLog struct {
	cli *redis.Client
	key string
	max int64
}

func NewEventLog(cli *redis.Client, key string, max int64) *EventLog {
	return &EventLog{cli: cli, key: key, max: max}
}

// Append 单条事件一行 JSON；失败只丢这条事件，绝不影响请求响应。
func (l *EventLog) Append(ctx context.Context, line []byte) error {
	pipe := l.cli.Pipeline()
	pipe.LPush(ctx, l.key, line)
	pipe.LTrim(ctx, l.key, 0, l.max-1)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("event append: %w", err)
	}
	return nil
}
