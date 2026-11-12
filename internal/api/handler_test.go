package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"mini-recommender/internal/api"
	"mini-recommender/internal/cache"
	"mini-recommender/internal/route"
	"mini-recommender/internal/store"
)

// 下面的 fake 展示了 Go 的隐式接口实现：结构体方法签名与 store.RouteStore、
// cache.Cache、twotower.Encoder 一致就自动满足接口，无需 implements 声明，
// 测试里因此能替换真实的 ES、Redis 和 Python 推理服务。

type fakeStore struct {
	routes     map[string]route.Route
	knn        []route.Route
	rules      []route.Route
	err        error
	knnErr     error
	getCalls   int
	rulesCalls int
	knnCalls   [][]string // 记录每次 KNN 的 exclude 列表
}

func (f *fakeStore) GetRoute(_ context.Context, id string) (*route.Route, error) {
	f.getCalls++
	if f.err != nil {
		return nil, f.err
	}
	r, ok := f.routes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (f *fakeStore) SearchRulesCandidates(_ context.Context, _ route.Route, _ []string, _ int) ([]route.Route, error) {
	f.rulesCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rules, nil
}

func (f *fakeStore) KNN(_ context.Context, _ []float32, exclude []string, _, _ int) ([]route.Route, error) {
	f.knnCalls = append(f.knnCalls, append([]string{}, exclude...))
	if f.knnErr != nil {
		return nil, f.knnErr
	}
	return f.knn, nil
}

func (f *fakeStore) SearchAllRoutes(_ context.Context, _ int) ([]route.Route, error) {
	out := make([]route.Route, 0, len(f.routes))
	for _, r := range f.routes {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) SearchRoutes(_ context.Context, cityID, tag, _ string, _ int) ([]route.Route, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []route.Route
	for _, r := range f.routes {
		if cityID != "" && r.CityID != cityID {
			continue
		}
		if tag != "" && !slices.Contains(r.Tags, tag) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type fakeCache struct {
	data map[string][]byte
	err  error
	sets []string // 记录所有写过的 key
}

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.data[key]
	if !ok {
		return nil, cache.ErrCacheMiss
	}
	return b, nil
}

func (f *fakeCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	f.sets = append(f.sets, key)
	if f.err != nil {
		return f.err
	}
	if f.data == nil {
		f.data = map[string][]byte{}
	}
	f.data[key] = val
	return nil
}

type fakeEncoder struct {
	qv  []float32
	err error
}

func (f *fakeEncoder) EncodeQuery(_ context.Context, _ route.Route, _ []route.Route) ([]float32, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.qv, "v1", nil
}

func (f *fakeEncoder) EncodeItem(_ context.Context, _ route.Route) ([]float32, string, error) {
	return []float32{1, 0}, "v1", nil
}

// fakeEventLog 记录 Append 收到的埋点行数。
type fakeEventLog struct {
	lines [][]byte
	err   error
}

func (f *fakeEventLog) Append(_ context.Context, line []byte) error {
	if f.err != nil {
		return f.err
	}
	f.lines = append(f.lines, line)
	return nil
}

var (
	penangCafe   = route.Route{RouteID: "penang-cafe-walk", Title: "咖啡馆散步", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee", "walking"}}
	penangArt    = route.Route{RouteID: "penang-art-walk", Title: "壁画散步", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"street-art", "walking"}}
	melakaRiver  = route.Route{RouteID: "melaka-river-cruise", Title: "马六甲河游船", CountryCode: "MY", CityID: "MY:malacca", Tags: []string{"heritage", "cruise"}}
	kyotoWalk    = route.Route{RouteID: "kyoto-gion-walk", Title: "祇园散步", CountryCode: "JP", CityID: "JP:kyoto", Tags: []string{"walking", "heritage"}}
)

func newTestHandler(st *fakeStore, cch *fakeCache, enc *fakeEncoder) *api.Handler {
	return api.NewHandler(st, cch, enc, &fakeEventLog{}, 5*time.Minute,
		api.DepTimeouts{Redis: 200 * time.Millisecond, ES: 500 * time.Millisecond, Python: 300 * time.Millisecond},
		"v1")
}

func doPost(h *api.Handler, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/recommendations", h.Recommendations)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/recommendations", strings.NewReader(body)))
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	return m
}

func TestTwoTowerFlowAndCacheFill(t *testing.T) {
	st := &fakeStore{
		routes: map[string]route.Route{penangCafe.RouteID: penangCafe, penangArt.RouteID: penangArt},
		// ANN 返回：1 条同城 + 1 条同国 + 1 条其他国家（规则排序应只保留前两类，同城在前）
		knn: []route.Route{melakaRiver, penangArt, kyotoWalk},
	}
	cch := &fakeCache{data: map[string][]byte{}}
	enc := &fakeEncoder{qv: []float32{0.1, 0.2}}

	rec := doPost(newTestHandler(st, cch, enc), `{"route_id":"penang-cafe-walk","recent_route_ids":["penang-art-walk"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["strategy"] != "two_tower" {
		t.Fatalf("strategy = %v, want two_tower", body["strategy"])
	}
	recs := body["recommendations"].([]any)
	if len(recs) != 2 {
		t.Fatalf("recommendations = %v", recs)
	}
	if recs[0].(map[string]any)["route_id"] != "penang-art-walk" || recs[1].(map[string]any)["route_id"] != "melaka-river-cruise" {
		t.Fatalf("同城应排第一、同国补充第二: %v", recs)
	}
	if recs[0].(map[string]any)["source"] != "two_tower" {
		t.Fatalf("每条推荐都要带来源: %v", recs[0])
	}
	if rec.Header().Get("X-ES-Recall") != "1" || rec.Header().Get("X-Strategy") != "two_tower" {
		t.Fatalf("headers = %v / %v", rec.Header().Get("X-ES-Recall"), rec.Header().Get("X-Strategy"))
	}
	// 已看路线和当前路线必须在 kNN 召回阶段就被排除
	if len(st.knnCalls) != 1 || !slices.Contains(st.knnCalls[0], "penang-art-walk") || !slices.Contains(st.knnCalls[0], "penang-cafe-walk") {
		t.Fatalf("knn exclude = %v", st.knnCalls)
	}
	// 结果应写入 tower 命名空间的缓存键
	if len(cch.sets) != 1 || !strings.HasPrefix(cch.sets[0], "rec:v2:tower:v1:") {
		t.Fatalf("cache sets = %v", cch.sets)
	}

	// 第二次相同请求应命中缓存，不再触达 ES / Python
	st2 := &fakeStore{routes: st.routes, knn: st.knn}
	rec2 := doPost(newTestHandler(st2, cch, enc), `{"route_id":"penang-cafe-walk","recent_route_ids":["penang-art-walk"]}`)
	if rec2.Header().Get("X-ES-Recall") != "0" || st2.getCalls != 0 {
		t.Fatalf("第二请求应走缓存: X-ES-Recall=%s getCalls=%d", rec2.Header().Get("X-ES-Recall"), st2.getCalls)
	}
}

func TestTwoTowerEmptyCandidates(t *testing.T) {
	st := &fakeStore{routes: map[string]route.Route{penangCafe.RouteID: penangCafe}, knn: nil}
	cch := &fakeCache{data: map[string][]byte{}}

	rec := doPost(newTestHandler(st, cch, &fakeEncoder{}), `{"route_id":"penang-cafe-walk"}`)
	body := decodeBody(t, rec)
	if body["strategy"] != "two_tower" {
		t.Fatalf("空候选策略仍应是 two_tower: %v", body["strategy"])
	}
	if string(mustMarshal(t, body["recommendations"])) != "[]" {
		t.Fatalf("recommendations 应为空数组: %v", body["recommendations"])
	}
}

func TestPythonDownFallbackToRules(t *testing.T) {
	st := &fakeStore{
		routes: map[string]route.Route{penangCafe.RouteID: penangCafe, penangArt.RouteID: penangArt},
		rules:  []route.Route{penangArt},
	}
	cch := &fakeCache{data: map[string][]byte{}}
	enc := &fakeEncoder{err: errors.New("python connection refused")}

	rec := doPost(newTestHandler(st, cch, enc), `{"route_id":"penang-cafe-walk","recent_route_ids":["kyoto-gion-walk"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["strategy"] != "rules" {
		t.Fatalf("应回退 rules: %v", body["strategy"])
	}
	// 缓存隔离：回退结果只能写 rules 命名空间，绝不能写 tower 键
	for _, key := range cch.sets {
		if strings.HasPrefix(key, "rec:v2:tower:") {
			t.Fatalf("规则回退结果污染了 tower 缓存键: %s", key)
		}
		if !strings.HasPrefix(key, "rec:v2:rules:") {
			t.Fatalf("未知缓存键: %s", key)
		}
	}

	// 再来一次相同请求：python 仍挂，先查 tower 缓存（miss）→ 取当前/已看路线 →
	// python 失败 → 命中 rules 缓存：规则召回不再触达 ES。
	st2 := &fakeStore{routes: st.routes, rules: st.rules}
	rec2 := doPost(newTestHandler(st2, cch, enc), `{"route_id":"penang-cafe-walk","recent_route_ids":["kyoto-gion-walk"]}`)
	if rec2.Header().Get("X-Strategy") != "rules" || rec2.Header().Get("X-ES-Recall") != "0" {
		t.Fatalf("第二请求应命中 rules 缓存: %v/%v", rec2.Header().Get("X-Strategy"), rec2.Header().Get("X-ES-Recall"))
	}
	if st2.rulesCalls != 0 {
		t.Fatalf("rules 缓存命中时不应做规则召回, rulesCalls=%d", st2.rulesCalls)
	}
}

func TestRouteNotFound(t *testing.T) {
	st := &fakeStore{routes: map[string]route.Route{}}
	cch := &fakeCache{data: map[string][]byte{}}

	rec := doPost(newTestHandler(st, cch, &fakeEncoder{}), `{"route_id":"no-such-route"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(cch.sets) != 0 {
		t.Fatalf("404 不应回填缓存: %v", cch.sets)
	}
}

func TestBadRequest(t *testing.T) {
	rec := doPost(newTestHandler(&fakeStore{}, &fakeCache{}, &fakeEncoder{}), `{"recent_route_ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 route_id 应 400, got %d", rec.Code)
	}
}

func TestESDown(t *testing.T) {
	st := &fakeStore{err: errors.New("es timeout")}
	cch := &fakeCache{data: map[string][]byte{}}

	rec := doPost(newTestHandler(st, cch, &fakeEncoder{}), `{"route_id":"penang-cafe-walk"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestGetByPathEndpoint(t *testing.T) {
	st := &fakeStore{
		routes: map[string]route.Route{penangCafe.RouteID: penangCafe, penangArt.RouteID: penangArt},
		knn:    []route.Route{penangArt},
	}
	h := newTestHandler(st, &fakeCache{data: map[string][]byte{}}, &fakeEncoder{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/items/{route_id}/recommendations", h.RecommendationsByPath)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/items/penang-cafe-walk/recommendations", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["strategy"] != "two_tower" {
		t.Fatalf("GET 入口也走统一流程: %v", body["strategy"])
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRequestIDForAttribution(t *testing.T) {
	st := &fakeStore{routes: map[string]route.Route{penangCafe.RouteID: penangCafe}, knn: []route.Route{penangArt}}
	rec := doPost(newTestHandler(st, &fakeCache{data: map[string][]byte{}}, &fakeEncoder{}), `{"route_id":"penang-cafe-walk"}`)
	body := decodeBody(t, rec)
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Fatalf("响应应包含 request_id 供埋点归因: %v", body)
	}
}

func TestRoutesList(t *testing.T) {
	st := &fakeStore{routes: map[string]route.Route{
		penangCafe.RouteID: penangCafe, penangArt.RouteID: penangArt, kyotoWalk.RouteID: kyotoWalk,
	}}
	h := newTestHandler(st, &fakeCache{data: map[string][]byte{}}, &fakeEncoder{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/routes", h.RoutesList)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/routes?city_id=MY:george-town", nil))
	body := decodeBody(t, rec)
	routes := body["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("按城市筛选应返回 2 条, got %d", len(routes))
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/routes?tag=coffee", nil))
	if n := len(decodeBody(t, rec2)["routes"].([]any)); n != 1 {
		t.Fatalf("按标签筛选应返回 1 条, got %d", n)
	}
}

func TestTrack(t *testing.T) {
	log := &fakeEventLog{}
	st := &fakeStore{routes: map[string]route.Route{penangCafe.RouteID: penangCafe}}
	h := api.NewHandler(st, &fakeCache{data: map[string][]byte{}}, &fakeEncoder{}, log, 5*time.Minute,
		api.DepTimeouts{Redis: 200 * time.Millisecond, ES: 500 * time.Millisecond, Python: 300 * time.Millisecond}, "v1")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/track", h.Track)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/track", strings.NewReader(
		`{"events":[{"type":"impression","route_id":"penang-art-walk","request_id":"abc","strategy":"two_tower","position":1}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(log.lines) != 1 || !strings.Contains(string(log.lines[0]), `"request_id":"abc"`) {
		t.Fatalf("事件应原样落日志: %s", log.lines)
	}
}
