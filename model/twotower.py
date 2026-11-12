# -*- coding: utf-8 -*-
"""双塔模型定义与样本构造，train.py / server.py / evaluate.py 共用。

未知标签处理规则（与 README 一致）：
- 编码时跳过不在词表里的标签；
- 一条路线没有任何已知标签时抛 NoKnownTags：离线 embed 跳过该路线，
  在线查询让 Go 回退规则推荐。物品塔只用标签特征，因此新导入的路线
  只要带已知标签就能立即算出向量参与 ANN，不需要重新训练。
"""
import json
import os
import random
from dataclasses import dataclass

import torch
import torch.nn as nn
import torch.nn.functional as F


class NoKnownTags(ValueError):
    """路线没有任何已知标签，无法编码。"""


def build_vocab(routes):
    """从全部路线的标签构建词表；排序后按位置当索引，保证可复现。"""
    tags = set()
    for r in routes:
        tags.update(r["tags"])
    return sorted(tags)


class TwoTower(nn.Module):
    """最小双塔：标签 Embedding 均值池化成路线表示，再各自投影到 32 维并 L2 归一化。

    用户塔（查询塔）：当前路线特征投影 cur_proj；历史路线各自编码后取平均再投影
    hist_proj；两者拼接过 combine 合成查询向量。没有历史时仅用当前路线。
    物品塔：只用候选路线的标签特征，item_proj 输出物品向量。
    两塔输出都是 32 维单位向量，用点积打分（等价 cosine）。
    """

    def __init__(self, vocab_size, tag_dim=16, out_dim=32):
        super().__init__()
        self.tag_emb = nn.Embedding(vocab_size, tag_dim)
        self.item_proj = nn.Linear(tag_dim, out_dim)
        self.cur_proj = nn.Linear(tag_dim, out_dim)
        self.hist_proj = nn.Linear(tag_dim, out_dim)
        self.combine = nn.Sequential(nn.Linear(2 * out_dim, out_dim), nn.ReLU())

    def encode_route(self, tag_ids):
        # 多条标签共享 Embedding 表，取均值作为路线级表示（mean pooling）。
        # 索引张量要显式放到与权重相同的设备上：模型在 GPU 时，CPU 索引会直接报错。
        ids = torch.tensor(tag_ids, device=self.tag_emb.weight.device)
        return self.tag_emb(ids).mean(dim=0)

    def _known_ids(self, tags, vocab):
        stoi = vocab if isinstance(vocab, dict) else {t: i for i, t in enumerate(vocab)}
        return [stoi[t] for t in tags if t in stoi]

    def item_vector(self, tags, vocab):
        ids = self._known_ids(tags, vocab)
        if not ids:
            raise NoKnownTags(f"no known tags: {tags}")
        v = self.item_proj(self.encode_route(ids))
        return F.normalize(v, p=2, dim=-1)

    def query_vector(self, current_tags, recent_tags_list, vocab):
        cur_ids = self._known_ids(current_tags, vocab)
        if not cur_ids:
            raise NoKnownTags(f"no known tags in current route: {current_tags}")
        cur = self.cur_proj(self.encode_route(cur_ids))
        known_hist = [self._known_ids(t, vocab) for t in recent_tags_list]
        known_hist = [ids for ids in known_hist if ids]
        if known_hist:
            hist_repr = torch.stack([self.encode_route(ids) for ids in known_hist]).mean(dim=0)
            hist = self.hist_proj(hist_repr)
            q = self.combine(torch.cat([cur, hist]))
        else:
            # 没有历史时，仅使用当前路线
            q = cur
        return F.normalize(q, p=2, dim=-1)


def forward_scores(model, sample, cand_tags_list, vocab):
    """一个样本对 1 正样本 + N 负样本做点积打分，返回分数向量。"""
    q = model.query_vector(sample["current"]["tags"], [r["tags"] for r in sample["history"]], vocab)
    scores = []
    for tags in cand_tags_list:
        v = model.item_vector(tags, vocab)
        scores.append(torch.dot(q, v))
    return torch.stack(scores)


@dataclass
class Sample:
    current: dict
    history: list  # 最多 5 条此前浏览路线
    positive: dict


def load_routes(data_dir):
    """读取数据目录下所有路线文件（sessions.json 除外）。"""
    routes = {}
    for name in sorted(os.listdir(data_dir)):
        if not name.endswith(".json") or name == "sessions.json":
            continue
        with open(os.path.join(data_dir, name), encoding="utf-8") as f:
            for r in json.load(f):
                routes[r["route_id"]] = r
    return routes


def load_sessions_split(sessions_path, routes, seed=42, test_ratio=0.2, max_history=5, negatives=4):
    """按完整会话划分训练/测试，再构造样本：

    - 每条样本：当前路线 + 此前最多 max_history 条浏览路线作为输入，下一条路线为正样本；
    - 负样本排除正样本、当前路线和已看路线，在全部路线中均匀采样；
    - 划分和负样本采样都用固定随机种子，训练与评估共用同一份划分，保证可复现。
    """
    with open(sessions_path, encoding="utf-8") as f:
        sessions = json.load(f)
    rng = random.Random(seed)
    order = sessions[:]
    rng.shuffle(order)
    n_test = max(1, int(len(order) * test_ratio))
    test_ids = {s["session_id"] for s in order[:n_test]}
    all_ids = sorted(routes.keys())

    def build(samples_out, session):
        seq = session["route_ids"]
        # 每条样本：当前路线 seq[i] + 此前最多 max_history 条历史（i=0 时无历史），正样本是下一条 seq[i+1]
        for i in range(0, len(seq) - 1):
            cur_id, pos_id = seq[i], seq[i + 1]
            if cur_id not in routes or pos_id not in routes:
                continue
            history = [routes[x] for x in seq[max(0, i - max_history):i] if x in routes]
            banned = {pos_id, cur_id} | {r["route_id"] for r in history}
            negs = rng.sample([x for x in all_ids if x not in banned], negatives)
            samples_out.append({
                "current": routes[cur_id],
                "history": history,
                "positive": routes[pos_id],
                "negatives": [routes[x] for x in negs],
            })

    train, test = [], []
    for s in sessions:
        build(test if s["session_id"] in test_ids else train, s)
    return train, test, sorted(test_ids)


def save_artifacts(out_dir, model, vocab, meta):
    os.makedirs(out_dir, exist_ok=True)
    torch.save(model.state_dict(), os.path.join(out_dir, "model.pt"))
    with open(os.path.join(out_dir, "vocab.json"), "w", encoding="utf-8") as f:
        json.dump(vocab, f, ensure_ascii=False, indent=1)
    with open(os.path.join(out_dir, "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)


def load_artifacts(model_dir, device):
    with open(os.path.join(model_dir, "meta.json"), encoding="utf-8") as f:
        meta = json.load(f)
    with open(os.path.join(model_dir, "vocab.json"), encoding="utf-8") as f:
        vocab = json.load(f)
    model = TwoTower(len(vocab), meta.get("tag_dim", 16), meta.get("out_dim", 32))
    state = torch.load(os.path.join(model_dir, "model.pt"), map_location=device)
    model.load_state_dict(state)
    model.to(device).eval()
    return model, vocab, meta


def get_device():
    return torch.device("cuda" if torch.cuda.is_available() else "cpu")
