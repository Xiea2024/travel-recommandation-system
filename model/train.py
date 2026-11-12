# -*- coding: utf-8 -*-
"""训练最小双塔模型。

用法:
    python model/train.py --data-dir data --sessions data/sessions.json \
        --out model/artifacts --version v1 --epochs 80 --seed 42

保存产物（--out/<version>/）:
    model.pt      模型权重
    vocab.json    标签词表（推理服务靠它把标签映射成索引）
    meta.json     模型版本、维度、种子、样本数等元信息
并在 --out 下写 LATEST 文件指向最新版本。
"""
import argparse
import datetime
import json
import os
import sys

import torch
import torch.nn.functional as F

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from twotower import (TwoTower, build_vocab, forward_scores, get_device,
                      load_artifacts, load_routes, load_sessions_split,
                      save_artifacts)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data-dir", default="data")
    ap.add_argument("--sessions", default="data/sessions.json")
    ap.add_argument("--out", default="model/artifacts")
    ap.add_argument("--version", default="v1")
    ap.add_argument("--epochs", type=int, default=80)
    ap.add_argument("--lr", type=float, default=1e-3)
    ap.add_argument("--negatives", type=int, default=4)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--test-ratio", type=float, default=0.2)
    args = ap.parse_args()

    torch.manual_seed(args.seed)

    routes = load_routes(args.data_dir)
    train, test, test_session_ids = load_sessions_split(
        args.sessions, routes, seed=args.seed, test_ratio=args.test_ratio, negatives=args.negatives)
    if not train or not test:
        sys.exit("训练/测试样本为空，请检查 sessions 数据")
    vocab = build_vocab(list(routes.values()))
    print(f"routes={len(routes)} vocab={len(vocab)} train_samples={len(train)} test_samples={len(test)} "
          f"test_sessions={test_session_ids}")

    device = get_device()
    print(f"device={device}" + (f" ({torch.cuda.get_device_name(0)})" if device.type == "cuda" else ""))
    model = TwoTower(len(vocab)).to(device)
    opt = torch.optim.Adam(model.parameters(), lr=args.lr)
    loss_fn = torch.nn.CrossEntropyLoss()  # 目标是候选列表里正样本的下标 0

    for epoch in range(1, args.epochs + 1):
        total, hit = 0.0, 0
        for s in train:
            cand_tags = [s["positive"]["tags"]] + [n["tags"] for n in s["negatives"]]
            scores = forward_scores(model, s, cand_tags, vocab)
            loss = loss_fn(scores.unsqueeze(0), torch.zeros(1, dtype=torch.long, device=scores.device))
            opt.zero_grad()
            loss.backward()
            opt.step()
            total += loss.item()
            hit += int(scores.argmax().item() == 0)
        if epoch % 20 == 0 or epoch == 1:
            print(f"epoch {epoch:3d} loss={total / len(train):.4f} train_acc@1={hit / len(train):.3f}")

    # 训练后在测试样本上看一眼双塔的 Recall@5（精确点积、全量候选），评估详见 evaluate.py
    with torch.no_grad():
        recalls = 0
        for s in test:
            cand = [s["positive"]] + s["negatives"]
            scores = forward_scores(model, s, [c["tags"] for c in cand], vocab)
            recalls += int(scores.argmax().item() == 0)
        print(f"test pos-vs-4neg accuracy={recalls / len(test):.3f}")

    meta = {
        "version": args.version,
        "out_dim": 32,
        "tag_dim": 16,
        "seed": args.seed,
        "trained_at": datetime.datetime.now().isoformat(timespec="seconds"),
        "n_routes": len(routes),
        "n_train_samples": len(train),
        "n_test_samples": len(test),
        "test_sessions": test_session_ids,
        "negatives_per_sample": args.negatives,
        "device_used": str(device),
    }
    out_dir = os.path.join(args.out, args.version)
    save_artifacts(out_dir, model, vocab, meta)
    with open(os.path.join(args.out, "LATEST"), "w") as f:
        f.write(args.version)
    print(f"saved artifacts to {out_dir}")


if __name__ == "__main__":
    main()
