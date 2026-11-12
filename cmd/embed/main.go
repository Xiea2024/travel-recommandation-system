// embed 命令离线计算路线向量并写回 Elasticsearch：
// 扫描索引里的全部路线 → 调 Python 推理服务的物品塔 /encode_item → bulk 更新 vector 字段 →
// 递增 Redis 里的 catalog:version，让 API 侧的双塔缓存键自动失效。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"mini-recommender/internal/route"
	"mini-recommender/internal/store"
	"mini-recommender/internal/twotower"
)

func main() {
	fs := flag.NewFlagSet("embed", flag.ExitOnError)
	esURL := fs.String("es", envOr("ES_URL", "http://localhost:9200"), "Elasticsearch 地址")
	index := fs.String("index", envOr("ES_INDEX", "routes"), "索引名")
	pythonURL := fs.String("python", envOr("MODEL_URL", "http://localhost:8000"), "Python 推理服务地址")
	redisAddr := fs.String("redis", envOr("REDIS_ADDR", "localhost:6379"), "Redis 地址")
	modelVersion := fs.String("model-version", envOr("MODEL_VERSION", "v1"), "模型版本（须与 Go API、Python 服务一致）")
	_ = fs.Parse(os.Args[1:])

	if err := run(*esURL, *index, *pythonURL, *redisAddr, *modelVersion); err != nil {
		fmt.Fprintf(os.Stderr, "embed failed: %v\n", err)
		os.Exit(1)
	}
}

func run(esURL, index, pythonURL, redisAddr, modelVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	es := store.NewES(esURL, index, &http.Client{Timeout: 10 * time.Second})
	enc := twotower.NewHTTPClient(pythonURL, &http.Client{Timeout: 10 * time.Second})

	routes, err := es.SearchAllRoutes(ctx, 10000)
	if err != nil {
		return fmt.Errorf("scan routes: %w", err)
	}
	fmt.Printf("scanned %d routes from ES\n", len(routes))

	// 逐条调物品塔编码。没有已知标签的路线拿不到向量（Python 返回 422），跳过并由规则召回兜底。
	vectors := make([]route.Route, 0, len(routes))
	skipped := 0
	for i := range routes {
		r := routes[i]
		v, ver, err := enc.EncodeItem(ctx, r)
		if err != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", r.RouteID, err)
			continue
		}
		if ver != modelVersion {
			return fmt.Errorf("版本不一致: Python 服务模型版本 %q != 期望 %q，请检查 MODEL_VERSION", ver, modelVersion)
		}
		r.Vector = v
		vectors = append(vectors, r)
	}
	if err := es.BulkUpdateVectors(ctx, vectors, modelVersion); err != nil {
		return fmt.Errorf("bulk update vectors: %w", err)
	}
	fmt.Printf("embedded %d routes (skipped %d), model_version=%s\n", len(vectors), skipped, modelVersion)

	// 递增目录版本：API 的缓存键含 catalog version，这里 +1 后旧的双塔推荐缓存自动失效。
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()
	n, err := rdb.Incr(ctx, "catalog:version").Result()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: bump catalog:version failed: %v（可手动 redis-cli INCR catalog:version）\n", err)
	} else {
		fmt.Printf("catalog:version -> %d\n", n)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
