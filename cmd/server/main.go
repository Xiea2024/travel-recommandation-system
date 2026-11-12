// 旅行路线推荐 API 服务入口：装配 ES / Redis / Python 推理客户端，监听 HTTP 并支持优雅退出。
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"mini-recommender/internal/api"
	"mini-recommender/internal/cache"
	"mini-recommender/internal/store"
	"mini-recommender/internal/twotower"
)

type config struct {
	Addr         string
	ESURL        string
	ESIndex      string
	RedisAddr    string
	RedisPass    string
	RedisDB      int
	ModelURL     string
	ModelVersion string
	CacheTTL     time.Duration
	RedisTMO     time.Duration
	ESTMO        time.Duration
	PythonTMO    time.Duration
}

func main() {
	cfg := loadConfig()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPass,
		DB:       cfg.RedisDB,
	})
	h := api.NewHandler(
		store.NewES(cfg.ESURL, cfg.ESIndex, nil),
		cache.NewRedisCache(rdb),
		twotower.NewHTTPClient(cfg.ModelURL, nil),
		cache.NewEventLog(rdb, "events", 50000),
		cfg.CacheTTL,
		api.DepTimeouts{Redis: cfg.RedisTMO, ES: cfg.ESTMO, Python: cfg.PythonTMO},
		cfg.ModelVersion,
	)

	// Go 1.22 起 ServeMux 支持“方法 + 路径模板”：{route_id} 在 handler 里用 r.PathValue 取出。
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/recommendations", h.Recommendations)
	mux.HandleFunc("GET /v1/items/{route_id}/recommendations", h.RecommendationsByPath)
	mux.HandleFunc("GET /v1/routes", h.RoutesList)
	mux.HandleFunc("GET /v1/routes/{route_id}", h.RouteDetail)
	mux.HandleFunc("POST /v1/track", h.Track)
	mux.HandleFunc("GET /metrics", h.Metrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second, // 整个请求的总预算；handler 里给每个下游依赖的 withTimeout 都是它的子预算，必须更短
		IdleTimeout:       60 * time.Second,
	}

	// signal.NotifyContext 监听 SIGINT/SIGTERM：K8s 删 Pod 时容器收到的 SIGTERM 会被转换成 ctx 的取消。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("route-recommender listening",
		"addr", cfg.Addr, "es", cfg.ESURL, "redis", cfg.RedisAddr,
		"model", cfg.ModelURL, "model_version", cfg.ModelVersion)

	<-ctx.Done() // 阻塞到收到退出信号
	slog.Info("shutdown signal received, draining in-flight requests")

	// Shutdown 先停止接收新连接，再等进行中的请求处理完；兜底超时到点强断，避免被 SIGKILL。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	slog.Info("bye")
}

// loadConfig 从环境变量读配置，给出本地开发可用的默认值。
func loadConfig() config {
	return config{
		Addr:         envOr("ADDR", ":8080"),
		ESURL:        envOr("ES_URL", "http://localhost:9200"),
		ESIndex:      envOr("ES_INDEX", "routes"),
		RedisAddr:    envOr("REDIS_ADDR", "localhost:6379"),
		RedisPass:    os.Getenv("REDIS_PASSWORD"),
		RedisDB:      envInt("REDIS_DB", 0),
		ModelURL:     envOr("MODEL_URL", "http://localhost:8000"),
		ModelVersion: envOr("MODEL_VERSION", "v1"),
		CacheTTL:     envDur("CACHE_TTL", 5*time.Minute),
		RedisTMO:     envDur("REDIS_TIMEOUT", 300*time.Millisecond),
		ESTMO:        envDur("ES_TIMEOUT", 800*time.Millisecond),
		PythonTMO:    envDur("PYTHON_TIMEOUT", 500*time.Millisecond),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
