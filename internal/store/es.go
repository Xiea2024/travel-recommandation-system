// Package store 封装 Elasticsearch 访问：路线读写、规则召回、双塔 ANN 向量召回、批量写向量。
// 只用标准库 net/http 直接调 ES REST API。
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mini-recommender/internal/route"
)

// ErrNotFound 表示路线在 Elasticsearch 中不存在，由 API 层转换成 404。
var ErrNotFound = errors.New("route not found")

// RouteStore 是 API 层依赖的抽象接口，测试时用内存 fake 替换真实 ES。
type RouteStore interface {
	GetRoute(ctx context.Context, id string) (*route.Route, error)
	// SearchRulesCandidates 召回同城或同国的候选（排除 exclude 中的路线）。
	SearchRulesCandidates(ctx context.Context, cur route.Route, exclude []string, size int) ([]route.Route, error)
	// KNN 用查询向量做近似最近邻召回，返回 cosine 相似度由高到低的路线。
	KNN(ctx context.Context, vector []float32, exclude []string, k int, numCandidates int) ([]route.Route, error)
	// SearchAllRoutes 返回索引中的全部路线（离线 embed 用）。
	SearchAllRoutes(ctx context.Context, size int) ([]route.Route, error)
	// SearchRoutes 按城市/标签/标题关键词筛选路线（前端发现页用）。
	SearchRoutes(ctx context.Context, cityID, tag, q string, size int) ([]route.Route, error)
}

// ES 实现了 RouteStore。
type ES struct {
	base   string // 形如 http://localhost:9200/routes
	client *http.Client
}

// Mapping 创建索引时使用的映射：vector 是 32 维 dense_vector，cosine 相似度，开启 HNSW 索引。
const Mapping = `{
  "mappings": {
    "properties": {
      "route_id":      { "type": "keyword" },
      "title":         { "type": "text" },
      "country_code":  { "type": "keyword" },
      "city_id":       { "type": "keyword" },
      "tags":          { "type": "keyword" },
      "vector":        { "type": "dense_vector", "dims": 32, "similarity": "cosine" },
      "vector_model":  { "type": "keyword" }
    }
  }
}`

func NewES(baseURL, index string, client *http.Client) *ES {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return &ES{base: strings.TrimRight(baseURL, "/") + "/" + index, client: client}
}

func (e *ES) GetRoute(ctx context.Context, id string) (*route.Route, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/_doc/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("es get route %q: %w", id, err)
	}
	// resp.Body 必须关闭且复用 TCP 连接；Do 失败时 resp 为 nil，所以 defer 要放在 err 判断之后。
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("es get route %q: status %d", id, resp.StatusCode)
	}

	// ES 单文档响应 {"_id":..., "_source":{...}}：匿名嵌套结构体 + JSON tag 逐层对应。
	var body struct {
		Source route.Route `json:"_source"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("es get route %q decode: %w", id, err)
	}
	if body.Source.RouteID == "" {
		body.Source.RouteID = id
	}
	return &body.Source, nil
}

// searchResp 对应 ES 搜索响应 {"hits":{"hits":[{"_id":...,"_source":...}]}} 的嵌套结构。
type searchResp struct {
	Hits struct {
		Hits []struct {
			ID     string       `json:"_id"`
			Source route.Route  `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func (e *ES) search(ctx context.Context, query map[string]any) ([]route.Route, error) {
	payload, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/_search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("es search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("es search: status %d: %s", resp.StatusCode, snippet)
	}
	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("es search decode: %w", err)
	}
	out := make([]route.Route, 0, len(sr.Hits.Hits))
	for _, h := range sr.Hits.Hits {
		hit := h.Source
		if hit.RouteID == "" {
			hit.RouteID = h.ID
		}
		out = append(out, hit)
	}
	return out, nil
}

// SearchRulesCandidates 构造同城 OR 同国的 bool 查询，并在 ES 侧排除当前路线和已看路线。
func (e *ES) SearchRulesCandidates(ctx context.Context, cur route.Route, exclude []string, size int) ([]route.Route, error) {
	return e.search(ctx, map[string]any{
		"size": size,
		"query": map[string]any{
			"bool": map[string]any{
				"must_not": map[string]any{"terms": map[string]any{"route_id": exclude}},
				"should": []any{
					map[string]any{"term": map[string]any{"city_id": cur.CityID}},
					map[string]any{"term": map[string]any{"country_code": cur.CountryCode}},
				},
				"minimum_should_match": 1,
			},
		},
	})
}

// KNN 构造 ES 8.x 的近似 kNN 查询：knn.query_vector 是 Python 推理服务返回的查询向量，
// knn.filter 在近似召回阶段就排除当前路线和已看路线（比召回后再过滤更省、也更准确）。
func (e *ES) KNN(ctx context.Context, vector []float32, exclude []string, k int, numCandidates int) ([]route.Route, error) {
	return e.search(ctx, map[string]any{
		"knn": map[string]any{
			"field":          "vector",
			"query_vector":   vector,
			"k":              k,
			"num_candidates": numCandidates,
			"filter": map[string]any{
				"bool": map[string]any{
					"must_not": map[string]any{"terms": map[string]any{"route_id": exclude}},
				},
			},
		},
		"_source": []string{"route_id", "title", "country_code", "city_id", "tags"},
	})
}

func (e *ES) SearchAllRoutes(ctx context.Context, size int) ([]route.Route, error) {
	return e.search(ctx, map[string]any{
		"size":  size,
		"query": map[string]any{"match_all": map[string]any{}},
	})
}

// SearchRoutes 组装筛选查询：city_id / tags 是精确匹配（filter，不参与算分），
// q 对标题做全文 match；全部条件为空时退回 match_all。
func (e *ES) SearchRoutes(ctx context.Context, cityID, tag, q string, size int) ([]route.Route, error) {
	boolQ := map[string]any{}
	var filters []any
	if cityID != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"city_id": cityID}})
	}
	if tag != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"tags": tag}})
	}
	if len(filters) > 0 {
		boolQ["filter"] = filters
	}
	if q != "" {
		// 统一搜索：标题全文匹配权重最高，标签精确匹配与 city_id 通配匹配也能命中。
		// filter 和 should 同时存在时，should 决定“哪些文档算命中”，filter 只限定范围。
		boolQ["should"] = []any{
			map[string]any{"match": map[string]any{"title": map[string]any{"query": q, "boost": 2}}},
			map[string]any{"term": map[string]any{"tags": q}},
			map[string]any{"wildcard": map[string]any{"city_id": "*" + strings.ToLower(q) + "*"}},
		}
		boolQ["minimum_should_match"] = 1
	}
	query := map[string]any{"match_all": map[string]any{}}
	if len(boolQ) > 0 {
		query = map[string]any{"bool": boolQ}
	}
	return e.search(ctx, map[string]any{
		"size":  size,
		"query": query,
		"sort":  []any{map[string]any{"route_id": "asc"}}, // 固定排序，前端翻页/刷新结果稳定
	})
}

// BulkUpdateVectors 批量把物品塔向量写回文档（{"update":{"_id":...}} + {"doc":{...}} 的 NDJSON 格式）。
// vector_model 记录生成向量的模型版本，查询侧必须与它一致。
func (e *ES) BulkUpdateVectors(ctx context.Context, routes []route.Route, modelVersion string) error {
	var buf bytes.Buffer
	for _, r := range routes {
		meta, _ := json.Marshal(map[string]any{"update": map[string]any{"_id": r.RouteID}})
		doc, _ := json.Marshal(map[string]any{"doc": map[string]any{
			"vector":       r.Vector,
			"vector_model": modelVersion,
		}})
		buf.Write(meta)
		buf.WriteByte('\n')
		buf.Write(doc)
		buf.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, e.base+"/_bulk?refresh=wait_for", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("es bulk update vectors: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var bulkResp struct {
		Errors bool `json:"errors"`
	}
	if err := json.Unmarshal(body, &bulkResp); err != nil {
		return fmt.Errorf("es bulk decode: %w", err)
	}
	if bulkResp.Errors {
		return fmt.Errorf("es bulk update vectors: some documents failed: %.300s", body)
	}
	return nil
}
