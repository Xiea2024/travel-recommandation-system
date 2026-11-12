// import 命令把路线数据文件写入 Elasticsearch。
// 文档 _id 固定为 route_id，重复执行即按固定 ID 覆盖更新；接入新地区只需准备新的数据文件再跑一次。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"mini-recommender/internal/route"
	"mini-recommender/internal/store"
)

func main() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	esURL := fs.String("es", envOr("ES_URL", "http://localhost:9200"), "Elasticsearch 地址")
	index := fs.String("index", envOr("ES_INDEX", "routes"), "索引名")
	reset := fs.Bool("reset", false, "删除索引重建（mapping 变更或清空数据时用）")
	var files stringList
	fs.Var(&files, "file", "路线数据文件（JSON 数组），可重复指定多个")
	_ = fs.Parse(os.Args[1:])

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "用法: import -file data/xx.json [-file ...] [-reset]")
		os.Exit(2)
	}
	if err := run(*esURL, *index, files, *reset); err != nil {
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		os.Exit(1)
	}
}

// stringList 实现 flag.Value，让 -file 可以重复指定。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func run(esURL, index string, files []string, reset bool) error {
	client := &http.Client{Timeout: 10 * time.Second}
	base := strings.TrimRight(esURL, "/") + "/" + index

	if reset {
		fmt.Println("reset: 删除旧索引")
		req, _ := http.NewRequest(http.MethodDelete, base, nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	// 建索引和 mapping（含 dense_vector 向量字段）；已存在时 ES 返回 400，忽略即可。
	if _, err := do(client, http.MethodPut, base, strings.NewReader(store.Mapping)); err != nil {
		fmt.Fprintf(os.Stderr, "warn: create index: %v（已存在则忽略）\n", err)
	}

	// 读取全部数据文件并做最小校验。
	var routes []route.Route
	for _, f := range files {
		items, err := loadFile(f)
		if err != nil {
			return err
		}
		routes = append(routes, items...)
	}
	seen := make(map[string]bool, len(routes))
	for _, r := range routes {
		if r.RouteID == "" || r.Title == "" || r.CountryCode == "" || r.CityID == "" {
			return fmt.Errorf("路线缺少必填字段（route_id/title/country_code/city_id）: %+v", r)
		}
		if seen[r.RouteID] {
			return fmt.Errorf("route_id 重复: %s", r.RouteID)
		}
		seen[r.RouteID] = true
	}

	// bulk 写入：NDJSON 格式，动作行指定固定 _id，重复导入时就是覆盖更新。
	var buf bytes.Buffer
	for _, r := range routes {
		meta, _ := json.Marshal(map[string]any{"index": map[string]any{"_id": r.RouteID}})
		doc, _ := json.Marshal(r)
		buf.Write(meta)
		buf.WriteByte('\n')
		buf.Write(doc)
		buf.WriteByte('\n')
	}
	body, err := do(client, http.MethodPut, base+"/_bulk?refresh=wait_for", &buf)
	if err != nil {
		return fmt.Errorf("bulk index: %w", err)
	}
	var bulkResp struct {
		Errors bool `json:"errors"`
	}
	if err := json.Unmarshal(body, &bulkResp); err != nil {
		return fmt.Errorf("parse bulk response: %w", err)
	}
	if bulkResp.Errors {
		return fmt.Errorf("bulk index: some documents failed: %.300s", body)
	}
	fmt.Printf("imported %d routes into %s\n", len(routes), base)
	fmt.Println("提示: 新导入的路线需要执行 embed 生成向量后才参与双塔召回（规则召回不受影响）")
	return nil
}

func loadFile(path string) ([]route.Route, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var items []route.Route
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return items, nil
}

// do 发出请求并读取整个响应体；非 2xx 一律视为错误，附上一段响应内容便于排查。
func do(client *http.Client, method, url string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: status %d: %.200s", method, url, resp.StatusCode, data)
	}
	return data, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
