package twotower_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mini-recommender/internal/route"
	"mini-recommender/internal/twotower"
)

// httptest.NewServer 假装 Python 推理服务：验证请求体结构和响应解析。

func TestEncodeQuery(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/encode_query" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vector":        []float32{0.1, 0.2},
			"model_version": "v1",
		})
	}))
	defer srv.Close()

	c := twotower.NewHTTPClient(srv.URL, srv.Client())
	cur := route.Route{RouteID: "a", Tags: []string{"coffee"}}
	recent := []route.Route{{RouteID: "b", Tags: []string{"walking"}}}
	vec, ver, err := c.EncodeQuery(context.Background(), cur, recent)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "v1" || len(vec) != 2 {
		t.Fatalf("ver=%s vec=%v", ver, vec)
	}
	var req map[string]any
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["current"].(map[string]any)["route_id"] != "a" || len(req["recent"].([]any)) != 1 {
		t.Fatalf("请求体 = %s", gotBody)
	}
}

func TestEncodeItemError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422) // 模拟“没有任何已知标签”
	}))
	defer srv.Close()

	c := twotower.NewHTTPClient(srv.URL, srv.Client())
	_, _, err := c.EncodeItem(context.Background(), route.Route{RouteID: "x", Tags: []string{"unknown"}})
	if err == nil {
		t.Fatal("422 应返回错误，让 Go 回退规则推荐")
	}
}
