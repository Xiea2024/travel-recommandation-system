package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mini-recommender/internal/route"
	"mini-recommender/internal/store"
)

// httptest.NewServer 在本地随机端口起真实 HTTP 服务，返回预置响应假装 ES，
// 测试真实的 HTTP 往返、错误映射和 JSON 解析。

const docResp = `{"_index":"routes","_id":"penang-cafe-walk","found":true,"_source":{
  "route_id":"penang-cafe-walk","title":"咖啡馆散步","country_code":"MY",
  "city_id":"MY:george-town","tags":["coffee","walking"]}}`

func TestGetRoute(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantErr  error // errors.Is 要匹配的错误；nil 表示走状态码分支断言
		wantCity string
	}{
		{name: "命中", status: 200, body: docResp, wantCity: "MY:george-town"},
		{name: "不存在", status: 404, body: `{"found":false}`, wantErr: store.ErrNotFound},
		{name: "ES 内部错误", status: 500, body: `{"error":"boom"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			es := store.NewES(srv.URL, "routes", srv.Client())
			got, err := es.GetRoute(context.Background(), "penang-cafe-walk")

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want errors.Is(..., %v)", err, tt.wantErr)
				}
				return
			}
			if tt.status == 200 {
				if err != nil || got == nil || got.CityID != tt.wantCity {
					t.Fatalf("got %+v, err %v, want city_id %q", got, err, tt.wantCity)
				}
			} else if err == nil {
				t.Fatalf("status %d 时期望返回错误", tt.status)
			}
		})
	}
}

// decodeQuery 捕获最后一次 _search 的请求体，供断言查询结构。
type queryCaptor struct {
	last []byte
	t    *testing.T
	resp string
}

func (c *queryCaptor) handler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/routes/_search") {
		c.t.Errorf("path = %s, want /routes/_search", r.URL.Path)
	}
	body, _ := io.ReadAll(r.Body)
	c.last = body
	_, _ = io.WriteString(w, c.resp)
}

func TestSearchRulesCandidatesQuery(t *testing.T) {
	cap := &queryCaptor{t: t, resp: `{"hits":{"hits":[]}}`}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()

	es := store.NewES(srv.URL, "routes", srv.Client())
	cur := route.Route{RouteID: "penang-cafe-walk", CountryCode: "MY", CityID: "MY:george-town"}
	if _, err := es.SearchRulesCandidates(context.Background(), cur, []string{"penang-cafe-walk", "x"}, 50); err != nil {
		t.Fatal(err)
	}

	var q map[string]any
	if err := json.Unmarshal(cap.last, &q); err != nil {
		t.Fatal(err)
	}
	boolQ := q["query"].(map[string]any)["bool"].(map[string]any)
	should := boolQ["should"].([]any)
	if len(should) != 2 { // 同城 OR 同国
		t.Fatalf("should = %v", should)
	}
	mustNot := boolQ["must_not"].(map[string]any)["terms"].(map[string]any)["route_id"].([]any)
	if len(mustNot) != 2 {
		t.Fatalf("exclude = %v", mustNot)
	}
}

func TestKNNQuery(t *testing.T) {
	cap := &queryCaptor{t: t, resp: `{"hits":{"hits":[{"_id":"a","_source":{"route_id":"a","title":"A"}}]}}`}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()

	es := store.NewES(srv.URL, "routes", srv.Client())
	qv := []float32{0.1, 0.2, 0.3}
	got, err := es.KNN(context.Background(), qv, []string{"cur", "seen"}, 50, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RouteID != "a" {
		t.Fatalf("got %+v", got)
	}

	var q map[string]any
	if err := json.Unmarshal(cap.last, &q); err != nil {
		t.Fatal(err)
	}
	knn, ok := q["knn"].(map[string]any)
	if !ok {
		t.Fatalf("请求体应包含顶层 knn 字段: %s", cap.last)
	}
	if knn["field"] != "vector" || knn["k"] != float64(50) || knn["num_candidates"] != float64(100) {
		t.Fatalf("knn = %v", knn)
	}
	vec := knn["query_vector"].([]any)
	if len(vec) != 3 {
		t.Fatalf("query_vector = %v", vec)
	}
	filter := knn["filter"].(map[string]any)["bool"].(map[string]any)["must_not"].(map[string]any)
	if filter["terms"].(map[string]any)["route_id"] == nil {
		t.Fatalf("knn filter = %v", filter)
	}
}

func TestBulkUpdateVectors(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"errors":false}`)
	}))
	defer srv.Close()

	es := store.NewES(srv.URL, "routes", srv.Client())
	routes := []route.Route{{RouteID: "r1", Vector: []float32{1, 2}}}
	if err := es.BulkUpdateVectors(context.Background(), routes, "v1"); err != nil {
		t.Fatal(err)
	}
	body := string(got)
	if !strings.Contains(body, `"update"`) || !strings.Contains(body, `"vector_model":"v1"`) {
		t.Fatalf("bulk 体不正确: %s", body)
	}
}

func TestSearchRoutesUnifiedQuery(t *testing.T) {
	cap := &queryCaptor{t: t, resp: `{"hits":{"hits":[]}}`}
	srv := httptest.NewServer(http.HandlerFunc(cap.handler))
	defer srv.Close()

	es := store.NewES(srv.URL, "routes", srv.Client())
	if _, err := es.SearchRoutes(context.Background(), "", "", "Kyoto", 100); err != nil {
		t.Fatal(err)
	}
	var q map[string]any
	if err := json.Unmarshal(cap.last, &q); err != nil {
		t.Fatal(err)
	}
	should := q["query"].(map[string]any)["bool"].(map[string]any)["should"].([]any)
	if len(should) != 3 { // 标题 match + 标签 term + city_id wildcard
		t.Fatalf("统一搜索应有三路 should: %s", cap.last)
	}
}
