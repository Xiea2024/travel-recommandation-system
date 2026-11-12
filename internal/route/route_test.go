package route_test

import (
	"slices"
	"testing"

	"mini-recommender/internal/route"
)

var cur = route.Route{
	RouteID:     "penang-cafe-walk",
	Title:       "乔治市咖啡馆散步",
	CountryCode: "MY",
	CityID:      "MY:george-town",
	Tags:        []string{"coffee", "walking"},
}

func TestRulesRank(t *testing.T) {
	tests := []struct {
		name       string
		candidates []route.Route
		exclude    map[string]bool
		wantIDs    []string
	}{
		{
			name: "同城优先于同国",
			candidates: []route.Route{
				{RouteID: "melaka-food", CountryCode: "MY", CityID: "MY:malacca", Tags: []string{"food", "walking", "heritage"}},
				{RouteID: "penang-art", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"street-art"}},
			},
			wantIDs: []string{"penang-art", "melaka-food"},
		},
		{
			name: "同层内标签重合多的排前面",
			candidates: []route.Route{
				{RouteID: "b-kl", CountryCode: "MY", CityID: "MY:kuala-lumpur", Tags: []string{"walking"}},
				{RouteID: "a-kl", CountryCode: "MY", CityID: "MY:kuala-lumpur", Tags: []string{"coffee", "walking"}},
			},
			wantIDs: []string{"a-kl", "b-kl"},
		},
		{
			name: "平分按 route_id 升序",
			candidates: []route.Route{
				{RouteID: "penang-z", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "penang-a", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"walking"}},
			},
			wantIDs: []string{"penang-a", "penang-z"},
		},
		{
			name: "其他国家不返回",
			candidates: []route.Route{
				{RouteID: "kyoto-walk", CountryCode: "JP", CityID: "JP:kyoto", Tags: []string{"coffee", "walking"}},
				{RouteID: "kl-walk", CountryCode: "MY", CityID: "MY:kuala-lumpur", Tags: []string{"walking"}},
			},
			wantIDs: []string{"kl-walk"},
		},
		{
			name: "排除当前路线和已看路线",
			candidates: []route.Route{
				{RouteID: "penang-cafe-walk", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "seen-1", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "penang-ok", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
			},
			exclude: map[string]bool{"penang-cafe-walk": true, "seen-1": true},
			wantIDs: []string{"penang-ok"},
		},
		{
			name:       "空候选返回空列表",
			candidates: nil,
			wantIDs:    []string{},
		},
		{
			name: "只返回前 limit 条",
			candidates: []route.Route{
				{RouteID: "p1", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "p2", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "p3", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "p4", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "p5", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
				{RouteID: "p6", CountryCode: "MY", CityID: "MY:george-town", Tags: []string{"coffee"}},
			},
			wantIDs: []string{"p1", "p2", "p3", "p4", "p5"},
		},
	}

	for _, tt := range tests {
		// t.Run 为每组输入创建独立子测试：失败信息带用例名，可 -run 'TestRulesRank/xxx' 单独执行。
		t.Run(tt.name, func(t *testing.T) {
			got := route.RulesRank(cur, tt.candidates, tt.exclude, 5)
			ids := make([]string, 0, len(got))
			for _, r := range got {
				ids = append(ids, r.RouteID)
			}
			if !slices.Equal(ids, tt.wantIDs) {
				t.Fatalf("got order %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}
