# -*- coding: utf-8 -*-
"""最小 Python 推理服务：加载双塔模型，接收路线特征，返回查询向量 / 物品向量。

端点:
    POST /encode_query  {"current": {"route_id","tags"}, "recent": [{"route_id","tags"}]}
    POST /encode_item   {"route_id","tags"}
    GET  /healthz
没有已知标签的路线返回 422，Go 侧会据此回退规则推荐。

环境变量: MODEL_DIR（默认 model/artifacts/v1）、PORT（默认 8000）。
只用标准库 HTTP 服务，除 PyTorch 外无其他依赖。
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import torch

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from twotower import NoKnownTags, get_device, load_artifacts

MODEL_DIR = os.environ.get("MODEL_DIR", "model/artifacts/v1")
PORT = int(os.environ.get("PORT", "8000"))

device = get_device()
model, vocab, meta = load_artifacts(MODEL_DIR, device)
print(f"loaded model {meta['version']} from {MODEL_DIR}, device={device}", flush=True)


def route_features(d):
    return {"route_id": d.get("route_id", ""), "tags": d.get("tags", [])}


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/healthz":
            self._send(200, {"ok": True, "model_version": meta["version"], "device": str(device)})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", 0))
            req = json.loads(self.rfile.read(length) or b"{}")
            if self.path == "/encode_query":
                current = route_features(req.get("current", {}))
                recent = [route_features(x) for x in req.get("recent", [])][:5]
                with torch.no_grad():
                    v = model.query_vector(current["tags"], [r["tags"] for r in recent], vocab)
            elif self.path == "/encode_item":
                r = route_features(req)
                with torch.no_grad():
                    v = model.item_vector(r["tags"], vocab)
            else:
                self._send(404, {"error": "not found"})
                return
            self._send(200, {"vector": v.tolist(), "model_version": meta["version"]})
        except NoKnownTags as e:
            self._send(422, {"error": str(e)})
        except (json.JSONDecodeError, KeyError, TypeError) as e:
            self._send(400, {"error": f"bad request: {e}"})

    def log_message(self, *args):  # 压测时静音访问日志
        pass


if __name__ == "__main__":
    srv = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(f"listening on :{PORT}", flush=True)
    srv.serve_forever()
