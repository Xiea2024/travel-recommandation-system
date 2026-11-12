# -*- coding: utf-8 -*-
"""评估：在留出（按完整会话划分的）测试样本上比较
  1) 规则推荐 vs 双塔（精确点积、全量候选）的 Recall@5；
  2) 同一批查询向量、相同候选范围下，ES ANN 与全量精确点积的 Top-5 重合率。

规则排序逻辑与 Go 的 route.RulesRank 完全一致：
同城（tier 0）→ 同国（tier 1）→ 其他国家不要；层内标签重合数降序、route_id 升序。
两种推荐都排除当前路线和已看（历史）路线。

用法:
    python model/evaluate.py --data-dir data --sessions data/sessions.json \
        --model-dir model/artifacts/v1 --es http://localhost:9200/routes
"""
import argparse
import json
import os
import sys
import urllib.request

import torch

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from twotower import load_artifacts, load_routes, load_sessions_split


def rules_rank(cur, candidates, exclude, k=5):
    """与 Go 版 RulesRank 对齐的规则排序（Python 镜像，用于对照实验）。"""
    out = []
    for c in candidates:
        if c["route_id"] in exclude:
            continue
        if c["city_id"] == cur["city_id"]:
            tier = 0
        elif c["country_code"] == cur["country_code"]:
            tier = 1
        else:
            continue
        overlap = len(set(c["tags"]) & set(cur["tags"]))
        out.append((tier, -overlap, c["route_id"], c))
    out.sort(key=lambda x: (x[0], x[1], x[2]))
    return [x[3] for x in out[:k]]


def exact_topk(model, vocab, sample, routes, exclude, k=5):
    """查询向量对全部物品向量做精确点积，返回排除后的 Top-k。"""
    q = model.query_vector(sample["current"]["tags"], [r["tags"] for r in sample["history"]], vocab)
    with torch.no_grad():  # 评估不需要梯度，关掉 autograd 能把速度拉开一个量级
        scored = []
        for r in routes.values():
            if r["route_id"] in exclude:
                continue
            v = model.item_vector(r["tags"], vocab)
            scored.append((float(torch.dot(q, v)), r))
    scored.sort(key=lambda x: (-x[0], x[1]["route_id"]))
    return [r for _, r in scored[:k]]


def es_knn(es_base, vector, exclude, k=5, num_candidates=100):
    """对线上同一个索引发起近似 kNN，与精确点积用同一批查询向量、同一候选范围（全库）。"""
    body = json.dumps({
        "knn": {
            "field": "vector",
            "query_vector": vector,
            "k": k,
            "num_candidates": num_candidates,
            "filter": {"bool": {"must_not": [{"terms": {"route_id": exclude}}]}},
        },
        "_source": ["route_id"],
    }).encode("utf-8")
    req = urllib.request.Request(es_base + "/_search", data=body,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        data = json.loads(resp.read())
    return [h["_source"]["route_id"] for h in data["hits"]["hits"]]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data-dir", default="data")
    ap.add_argument("--sessions", default="data/sessions.json")
    ap.add_argument("--model-dir", default="model/artifacts/v1")
    ap.add_argument("--es", default="http://localhost:9200/routes")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--test-ratio", type=float, default=0.2)
    args = ap.parse_args()

    device = torch.device("cpu")  # 评估量小，CPU 足够
    model, vocab, meta = load_artifacts(args.model_dir, device)
    routes = load_routes(args.data_dir)
    _, test, _ = load_sessions_split(args.sessions, routes, seed=args.seed, test_ratio=args.test_ratio)

    rules_hit = tower_hit = overlap_sum = identical = 0
    for s in test:
        exclude = {s["current"]["route_id"]} | {r["route_id"] for r in s["history"]}
        pos_id = s["positive"]["route_id"]

        r_top5 = [r["route_id"] for r in rules_rank(s["current"], routes.values(), exclude)]
        rules_hit += int(pos_id in r_top5)

        t_top5 = [r["route_id"] for r in exact_topk(model, vocab, s, routes, exclude)]
        tower_hit += int(pos_id in t_top5)

        q = model.query_vector(s["current"]["tags"], [r["tags"] for r in s["history"]], vocab)
        ann_top5 = es_knn(args.es, q.tolist(), list(exclude))
        overlap_sum += len(set(ann_top5) & set(t_top5))
        identical += int(ann_top5 == t_top5)

    n = len(test)
    result = {
        "test_samples": n,
        "data_source": "data/sessions.json 人工编写的模拟浏览会话（按完整会话划分留出测试集）",
        "model_version": meta["version"],
        "rules_recall@5": round(rules_hit / n, 4),
        "two_tower_recall@5_exact": round(tower_hit / n, 4),
        "ann_vs_exact_top5_overlap": round(overlap_sum / n / 5, 4),   # 平均每条查询重合几个
        "ann_vs_exact_top5_identical_rate": round(identical / n, 4),  # 完全一致的占比
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
