// Package route 定义路线数据模型和规则排序逻辑（双塔失败时的对照与回退策略）。
package route

import (
	"cmp"
	"slices"
)

// SourceTwoTower / SourceRules 是推荐结果的两个策略来源，会原样返回给调用方。
const (
	SourceTwoTower = "two_tower"
	SourceRules    = "rules"
)

// Route 是 Elasticsearch 中存储的一条旅行路线。
// Vector / VectorModel 由离线 embed 命令写入，查询塔与向量索引必须使用同一 ModelVersion。
type Route struct {
	RouteID     string    `json:"route_id"`
	Title       string    `json:"title"`
	CountryCode string    `json:"country_code"`
	CityID      string    `json:"city_id"`
	Tags        []string  `json:"tags"`
	ImageURL    string    `json:"image_url,omitempty"`
	Vector      []float32 `json:"vector,omitempty"`
	VectorModel string    `json:"vector_model,omitempty"`
}

// Recommendation 是返回给调用方的一条推荐。
type Recommendation struct {
	RouteID string `json:"route_id"`
	Title   string `json:"title"`
	Source  string `json:"source"`
}

// RulesRank 是规则推荐排序：同城优先、同国补充，其余不返回；
// 同层内按标签重合数降序、route_id 升序，保证结果稳定可复现。
// exclude 里是必须排除的路线（当前路线 + 已看路线），即使它出现在候选里也会被丢掉。
func RulesRank(cur Route, candidates []Route, exclude map[string]bool, limit int) []Route {
	type scored struct {
		r     Route
		tier  int // 0 同城，1 同国，2 其他国家
		tags  int
	}
	list := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if exclude[c.RouteID] {
			continue
		}
		s := scored{r: c, tier: 2}
		if c.CityID == cur.CityID {
			s.tier = 0
		} else if c.CountryCode == cur.CountryCode {
			s.tier = 1
		} else {
			continue // 规则推荐只覆盖同城和同国
		}
		for _, t := range c.Tags {
			if slices.Contains(cur.Tags, t) {
				s.tags++
			}
		}
		list = append(list, s)
	}

	// 与 slices.SortFunc 的约定一致：负数 a 前 b 后，正数相反，0 并列取下一排序键。
	slices.SortFunc(list, func(a, b scored) int {
		if c := cmp.Compare(a.tier, b.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(b.tags, a.tags); c != 0 { // 标签重合数降序
			return c
		}
		return cmp.Compare(a.r.RouteID, b.r.RouteID) // 平分按 route_id 升序
	})

	out := make([]Route, 0, min(limit, len(list)))
	for _, s := range list {
		if len(out) >= limit {
			break
		}
		out = append(out, s.r)
	}
	return out
}
