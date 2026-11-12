// Package twotower 封装对 Python 双塔推理服务的 HTTP 调用。
package twotower

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mini-recommender/internal/route"
)

// Encoder 抽象双塔推理服务：查询塔（当前路线 + 历史路线 → 查询向量）和物品塔（路线 → 向量）。
// 测试时用内存 fake 替换真实的 Python 服务。
type Encoder interface {
	// EncodeQuery 返回查询向量与模型版本；任何失败（超时、5xx、网络错误）都返回 error，
	// 调用方据此回退到规则推荐。
	EncodeQuery(ctx context.Context, current route.Route, recent []route.Route) ([]float32, string, error)
	EncodeItem(ctx context.Context, r route.Route) ([]float32, string, error)
}

// HTTPClient 用标准库调用 Python 推理服务。
type HTTPClient struct {
	base   string
	client *http.Client
}

func NewHTTPClient(base string, client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return &HTTPClient{base: strings.TrimRight(base, "/"), client: client}
}

type features struct {
	RouteID string   `json:"route_id"`
	Tags    []string `json:"tags"`
}

type encodeResp struct {
	Vector       []float32 `json:"vector"`
	ModelVersion string    `json:"model_version"`
}

// post 发请求并解析响应；Python 服务任何非 2xx（比如没有已知标签时的 422）都视为失败。
func (c *HTTPClient) post(ctx context.Context, path string, reqBody any) ([]float32, string, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("two-tower %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("two-tower %s: status %d", path, resp.StatusCode)
	}
	var er encodeResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, "", fmt.Errorf("two-tower %s decode: %w", path, err)
	}
	return er.Vector, er.ModelVersion, nil
}

func (c *HTTPClient) EncodeQuery(ctx context.Context, current route.Route, recent []route.Route) ([]float32, string, error) {
	hist := make([]features, 0, len(recent))
	for _, r := range recent {
		hist = append(hist, features{RouteID: r.RouteID, Tags: r.Tags})
	}
	return c.post(ctx, "/encode_query", map[string]any{
		"current": features{RouteID: current.RouteID, Tags: current.Tags},
		"recent":  hist,
	})
}

func (c *HTTPClient) EncodeItem(ctx context.Context, r route.Route) ([]float32, string, error) {
	return c.post(ctx, "/encode_item", features{RouteID: r.RouteID, Tags: r.Tags})
}
