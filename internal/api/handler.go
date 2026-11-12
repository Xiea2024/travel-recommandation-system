// Package api 实现统一的推荐 HTTP 接口：
// 先查双塔缓存 → 未命中时 Go 取路线特征、Python 推理出查询向量、ES 近似 kNN 召回 →
// 同城优先、同国补充取前 5 → 回填缓存；Python 或 ES 失败时在剩余请求预算内回退规则推荐。
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mini-recommender/internal/cache"
	"mini-recommender/internal/route"
	"mini-recommender/internal/store"
	"mini-recommender/internal/twotower"
)

// DepTimeouts 是各下游依赖的短超时预算，整体应明显小于 http.Server 的 WriteTimeout。
type DepTimeouts struct {
	Redis  time.Duration
	ES     time.Duration
	Python time.Duration // 推理服务调用
}

// EventAppender 抽象埋点日志写入，测试时用内存 fake 替换 Redis。
type EventAppender interface {
	Append(ctx context.Context, line []byte) error
}

// Handler 实现推荐接口，依赖全部是小接口，便于测试替换。
type Handler struct {
	Store        store.RouteStore
	Cch          cache.Cache
	Enc          twotower.Encoder
	Events       EventAppender
	TTL          time.Duration
	TMO          DepTimeouts
	ModelVersion string // 与 Python 服务、ES 向量索引保持一致的模型版本

	esRecalls      atomic.Int64 // ES 召回次数
	cacheHits      atomic.Int64
	rulesFallbacks atomic.Int64
}

func NewHandler(st store.RouteStore, cch cache.Cache, enc twotower.Encoder, events EventAppender, ttl time.Duration, tmo DepTimeouts, modelVersion string) *Handler {
	return &Handler{Store: st, Cch: cch, Enc: enc, Events: events, TTL: ttl, TMO: tmo, ModelVersion: modelVersion}
}

// withTimeout 给单次下游调用派生一个短超时 ctx。
// 返回的 cancel 必须 defer 调用：WithTimeout 会启动一个计时 goroutine，不 cancel 就会泄漏到父 ctx 结束。
// 传入的 ctx 来自 r.Context()：客户端断开时，派生出的 Redis/ES/Python 调用会一起被取消。
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

type recRequest struct {
	RouteID        string   `json:"route_id"`
	RecentRouteIDs []string `json:"recent_route_ids"` // 可省略；最多取最近 5 条
}

type recResponse struct {
	RouteID         string                 `json:"route_id"`
	RequestID       string                 `json:"request_id"` // 埋点归因：前端曝光/点击事件带回来
	Strategy        string                 `json:"strategy"`   // two_tower | rules
	ModelVersion    string                 `json:"model_version,omitempty"`
	CatalogVersion  string                 `json:"catalog_version,omitempty"`
	Recommendations []route.Recommendation `json:"recommendations"`
}

const (
	knnK              = 50    // ANN 多召回一些，给同城/同国过滤留余量
	knnNumCandidates  = 100   // HNSW 遍历的候选数，越大越准越慢
	rulesRecallSize   = 50
	maxRecent         = 5     // 编码历史时最多使用的已看路线数
	catalogVersionKey = "catalog:version"
)

// Recommendations 处理 POST /v1/recommendations。
func (h *Handler) Recommendations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context() // 请求的 ctx，随客户端断开/服务端超时而取消，会传给 Redis、ES 和 Python

	var req recRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RouteID == "" {
		writeError(w, http.StatusBadRequest, "invalid request body: need route_id")
		return
	}
	h.recommend(ctx, w, req.RouteID, normalizeRecent(req.RecentRouteIDs, req.RouteID))
}

// RecommendationsByPath 处理 GET /v1/items/{route_id}/recommendations（规则路径的快捷入口，等价于不带历史的 POST）。
func (h *Handler) RecommendationsByPath(w http.ResponseWriter, r *http.Request) {
	h.recommend(r.Context(), w, r.PathValue("route_id"), nil)
}

// recommend 是两条 HTTP 入口共用的核心流程。
func (h *Handler) recommend(ctx context.Context, w http.ResponseWriter, routeID string, seen []string) {
	// 缓存键把“当前路线 + 有效历史 + 模型版本 + 目录版本”都编码进去：
	// 换模型、重新 embed（目录版本 +1）或换历史，都会落到不同的键，不会读到旧结果。
	catalog := h.catalogVersion(ctx)
	seenKey := hashIDs(seen)
	towerKey := fmt.Sprintf("rec:v2:tower:%s:%s:%s:%s", h.ModelVersion, catalog, routeID, seenKey)
	// 规则回退结果用独立的键命名空间：回退结果绝不写进 tower 键，避免模型恢复后读到陈旧规则结果。
	rulesKey := fmt.Sprintf("rec:v2:rules:%s:%s", routeID, seenKey)

	// ---- 1. 先查双塔缓存 ----
	getCtx, cancel := withTimeout(ctx, h.TMO.Redis)
	defer cancel()
	if payload, err := h.Cch.Get(getCtx, towerKey); err == nil {
		h.cacheHits.Add(1)
		w.Header().Set("X-Strategy", route.SourceTwoTower)
		w.Header().Set("X-ES-Recall", "0")
		writeRawJSON(w, http.StatusOK, payload)
		return
	} else if !errors.Is(err, cache.ErrCacheMiss) {
		slog.WarnContext(ctx, "redis unavailable, will not use cache", "err", err)
	}

	// ---- 2. 取当前路线，不存在返回 404（也不回填任何缓存）----
	esCtx, cancel := withTimeout(ctx, h.TMO.ES)
	defer cancel()
	cur, err := h.Store.GetRoute(esCtx, routeID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "get route failed", "route_id", routeID, "err", err)
		writeError(w, http.StatusBadGateway, "upstream unavailable")
		return
	}

	// ---- 3. 取已看路线的特征（编码查询向量要用；不存在的 id 直接忽略）----
	seenRoutes := make([]route.Route, 0, len(seen))
	exclude := make([]string, 0, len(seen)+1)
	exclude = append(exclude, routeID)
	for _, id := range seen {
		gctx, cancel := withTimeout(ctx, h.TMO.ES)
		r, err := h.Store.GetRoute(gctx, id)
		cancel()
		if err != nil {
			slog.WarnContext(ctx, "recent route unavailable, skipped", "route_id", id, "err", err)
			continue
		}
		exclude = append(exclude, id)
		seenRoutes = append(seenRoutes, *r)
	}

	// ---- 4. 双塔路径：Python 编码查询向量 → ES 近似 kNN ----
	recs, ok := h.recommendTwoTower(ctx, cur, seenRoutes, exclude)
	if ok {
		payload, err := json.Marshal(recResponse{
			RouteID:         routeID,
			RequestID:       newRequestID(),
			Strategy:        route.SourceTwoTower,
			ModelVersion:    h.ModelVersion,
			CatalogVersion:  catalog,
			Recommendations: recs,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		h.esRecalls.Add(1)
		h.fillCache(ctx, towerKey, payload)
		w.Header().Set("X-Strategy", route.SourceTwoTower)
		w.Header().Set("X-ES-Recall", "1")
		writeRawJSON(w, http.StatusOK, payload)
		return
	}

	// ---- 5. 回退规则路径：先看规则缓存，未命中再召回排序 ----
	h.rulesFallbacks.Add(1)
	getCtx2, cancel := withTimeout(ctx, h.TMO.Redis)
	defer cancel()
	if payload, err := h.Cch.Get(getCtx2, rulesKey); err == nil {
		h.cacheHits.Add(1)
		w.Header().Set("X-Strategy", route.SourceRules)
		w.Header().Set("X-ES-Recall", "0")
		writeRawJSON(w, http.StatusOK, payload)
		return
	}

	recs, err = h.recommendRules(ctx, cur, exclude)
	if err != nil {
		slog.ErrorContext(ctx, "rules fallback failed", "route_id", routeID, "err", err)
		writeError(w, http.StatusBadGateway, "upstream unavailable")
		return
	}
	payload, err := json.Marshal(recResponse{
		RouteID:         routeID,
		RequestID:       newRequestID(),
		Strategy:        route.SourceRules,
		CatalogVersion:  catalog,
		Recommendations: recs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.fillCache(ctx, rulesKey, payload)
	w.Header().Set("X-Strategy", route.SourceRules)
	w.Header().Set("X-ES-Recall", "1")
	writeRawJSON(w, http.StatusOK, payload)
}

// recommendTwoTower 返回 (推荐列表, true)；Python 或 ES 失败返回 (nil, false)，由调用方回退规则。
// 候选为空不是失败：按实际数量返回空列表，策略仍是 two_tower。
func (h *Handler) recommendTwoTower(ctx context.Context, cur *route.Route, seenRoutes []route.Route, exclude []string) ([]route.Recommendation, bool) {
	// Python 用独立的短超时（默认 500ms）：它是模型的边界，超时不拖累整体请求预算，失败后还有时间走规则召回。
	pyCtx, cancel := withTimeout(ctx, h.TMO.Python)
	defer cancel()
	qv, _, err := h.Enc.EncodeQuery(pyCtx, *cur, seenRoutes)
	if err != nil {
		slog.WarnContext(ctx, "two-tower encode failed, fallback to rules", "route_id", cur.RouteID, "err", err)
		return nil, false
	}

	knnCtx, cancel := withTimeout(ctx, h.TMO.ES)
	defer cancel()
	candidates, err := h.Store.KNN(knnCtx, qv, exclude, knnK, knnNumCandidates)
	if err != nil {
		slog.WarnContext(ctx, "es knn failed, fallback to rules", "route_id", cur.RouteID, "err", err)
		return nil, false
	}
	return toRecs(route.RulesRank(*cur, candidates, nil, 5), route.SourceTwoTower), true
}

// recommendRules 走 ES 布尔召回 + Go 本地规则排序。
func (h *Handler) recommendRules(ctx context.Context, cur *route.Route, exclude []string) ([]route.Recommendation, error) {
	esCtx, cancel := withTimeout(ctx, h.TMO.ES)
	defer cancel()
	candidates, err := h.Store.SearchRulesCandidates(esCtx, *cur, exclude, rulesRecallSize)
	if err != nil {
		return nil, err
	}
	h.esRecalls.Add(1)
	return toRecs(route.RulesRank(*cur, candidates, idSet(exclude), 5), route.SourceRules), nil
}

// fillCache 尽力回填缓存：失败只影响下次命中，不影响本次响应。
func (h *Handler) fillCache(ctx context.Context, key string, payload []byte) {
	setCtx, cancel := withTimeout(ctx, h.TMO.Redis)
	defer cancel()
	if err := h.Cch.Set(setCtx, key, payload, h.TTL); err != nil {
		slog.WarnContext(ctx, "cache fill failed", "key", key, "err", err)
	}
}

// catalogVersion 读取 embed 命令递增的目录版本号，用于缓存键失效；读不到时退回 "unknown"。
func (h *Handler) catalogVersion(ctx context.Context) string {
	getCtx, cancel := withTimeout(ctx, h.TMO.Redis)
	defer cancel()
	v, err := h.Cch.Get(getCtx, catalogVersionKey)
	if err != nil {
		return "unknown"
	}
	return string(v)
}

// RoutesList 处理 GET /v1/routes?city_id=&tag=&q= —— 前端发现页的筛选列表。
func (h *Handler) RoutesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), h.TMO.ES)
	defer cancel()
	q := r.URL.Query()
	size := 100
	if n, err := strconv.Atoi(q.Get("size")); err == nil && n > 0 && n <= 200 {
		size = n
	}
	routes, err := h.Store.SearchRoutes(ctx, q.Get("city_id"), q.Get("tag"), q.Get("q"), size)
	if err != nil {
		slog.ErrorContext(ctx, "list routes failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// RouteDetail 处理 GET /v1/routes/{route_id} —— 前端详情页加载单条路线。
func (h *Handler) RouteDetail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r.Context(), h.TMO.ES)
	defer cancel()
	rt, err := h.Store.GetRoute(ctx, r.PathValue("route_id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "get route failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream unavailable")
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

// Track 处理 POST /v1/track —— 前端埋点（曝光/点击），带 request_id 与 strategy 归因到推荐策略。
// 事件写入 Redis 列表，攒够后导出替换训练用的模拟会话；写入失败只丢事件，不影响响应。
func (h *Handler) Track(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Events []struct {
			Type      string `json:"type"`       // impression | click
			RouteID   string `json:"route_id"`   // 曝光/点击的路线
			RequestID string `json:"request_id"` // 推荐响应里的归因 ID
			Strategy  string `json:"strategy"`   // two_tower | rules（响应里的 source）
			Position  int    `json:"position"`   // 推荐位次序，从 1 开始
		} `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Events) == 0 {
		writeError(w, http.StatusBadRequest, "invalid events")
		return
	}
	ctx, cancel := withTimeout(r.Context(), h.TMO.Redis)
	defer cancel()
	now := time.Now().Format(time.RFC3339)
	for _, e := range req.Events[:min(len(req.Events), 100)] {
		line, err := json.Marshal(map[string]any{
			"ts": now, "type": e.Type, "route_id": e.RouteID,
			"request_id": e.RequestID, "strategy": e.Strategy, "position": e.Position,
		})
		if err != nil {
			continue
		}
		if err := h.Events.Append(ctx, line); err != nil {
			slog.WarnContext(ctx, "event append failed", "err", err)
			break // Redis 不可用就别再刷错误了
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// newRequestID 生成埋点归因 ID：纳秒时间戳 + 4 字节随机数的 hex，单机部署足够唯一。
func newRequestID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), b)
}

// Metrics 返回累计计数，供人工观察缓存命中与回退情况。
func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"es_recalls":      h.esRecalls.Load(),
		"cache_hits":      h.cacheHits.Load(),
		"rules_fallbacks": h.rulesFallbacks.Load(),
	})
}

// normalizeRecent 去重、去掉当前路线，并只保留最近 maxRecent 条，保证缓存键和编码输入稳定。
func normalizeRecent(ids []string, current string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for i := len(ids) - 1; i >= 0 && len(out) < maxRecent; i-- {
		id := ids[i]
		if id == current || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Reverse(out) // 恢复原先后顺序（旧 → 新）
	return out
}

// hashIDs 把已看路线列表编码成缓存键的一部分；列表为空时得到固定占位符。
func hashIDs(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ",") // 已 normalize：顺序稳定、无重复
}

func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func toRecs(rs []route.Route, source string) []route.Recommendation {
	out := make([]route.Recommendation, 0, len(rs))
	for _, r := range rs {
		out = append(out, route.Recommendation{RouteID: r.RouteID, Title: r.Title, Source: source})
	}
	return out
}

func writeRawJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		payload = []byte(`{"error":"marshal failed"}`)
	}
	writeRawJSON(w, status, payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
