# 旅行路线推荐 · Journey Recommendation

一个**通用旅行路线推荐系统**：给定当前路线和可选浏览历史，返回最多 5 条相似路线。
双塔模型 + Elasticsearch ANN 为主，规则引擎为对照与故障回退，配 React 前端、AI 生成封面和访客流量后台。

```
浏览器 (React :5173)
    │ POST /v1/recommendations
    ▼
Go API (:8080) ── Redis 缓存（双塔/规则键空间隔离）
    │ 未命中            │ 埋点事件
    ▼                   ▼
Python 推理 (:8000)   Redis（events 列表）
    │ 查询向量
    ▼
Elasticsearch (dense_vector kNN, cosine) ── Go embed 命令离线写物品向量
```

## 特性

- **双塔召回**：32 维查询向量（当前路线 + 最近 ≤5 条历史的标签编码），ES 8.x 近似 kNN（cosine）召回，同城优先、同国补充
- **规则回退**：同城 → 同国 → 标签重合 → ID 排序；Python/ES 故障时在剩余请求预算内自动降级，与双塔缓存键隔离
- **会话级个性化**：无账号体系，`recent_route_ids` 随请求传入即个性化（前端 localStorage 匿名维护）
- **AI 封面**：全部路线/城市封面由 Cloudflare Workers AI（FLUX.1 Schnell）生成，统一旅行画册插画风格，WebP 1024×576
- **数据飞轮**：前端埋点（曝光/点击带 request_id/strategy/坑位）入 Redis，可导出替换训练会话
- **内容**：马来西亚 32 条 + 京都 8 条 + 上海 12 条 + 厦门 10 条 = 62 条路线 / 12 城，接入新地区只需数据文件 + 三条命令

## 目录结构

```
cmd/server/        Go API 入口（优雅退出）
cmd/import/        路线数据导入（-file 可重复，固定 route_id 覆盖更新，-reset 重建索引）
cmd/embed/         离线物品塔编码：扫描 ES → 调 Python → 批量写向量 → INCR catalog:version
internal/route/    路线模型 + 规则排序（同城→同国→标签→ID）
internal/store/    ES 客户端（CRUD / 筛选 / kNN / 批量写向量，标准库 net/http）
internal/twotower/ Python 推理服务 HTTP 客户端
internal/api/      推荐 / 列表 / 详情 / 埋点 / 管理页 handler
internal/cache/    Redis 缓存 + 埋点事件日志
model/             PyTorch 双塔（train.py 训练 / server.py 推理 / evaluate.py 评估 / artifacts/v1 产物）
tools/gen_images.py  FLUX 封面批量生成（续跑、进度落盘、回填 image_url）
data/              路线数据（my/kyoto/cn_shanghai/cn_xiamen.json）+ sessions.json 模拟/真实会话
frontend/          React 18 + TS + Vite（发现页/城市页/搜索页/详情页）
deploy/            docker-compose.yml / k8s.yaml / Dockerfile / Dockerfile.model
frontend/public/images/  AI 生成的路线与城市封面（routes/ cities/）
```

## 快速开始

前置：Go 1.26+、Docker、Python 3.14 + PyTorch（GPU 可选）、Node 18+。

```bash
# 1. 依赖：ES + Redis
docker compose -f deploy/docker-compose.yml up -d elasticsearch redis

# 2. 导入路线数据
go run ./cmd/import -reset -file data/my.json -file data/kyoto.json -file data/cn_shanghai.json -file data/cn_xiamen.json

# 3. 双塔：已有训练产物 model/artifacts/v1 可直接用；重训才需要：
#    python model/train.py --epochs 100
python model/server.py                 # 推理服务 :8000（GPU 自动检测）

# 4. 向量入库（注意：每次 import 之后都要跑，导入会整文档覆盖冲掉向量）
go run ./cmd/embed

# 5. Go API
go run ./cmd/server                    # :8080

# 6. 前端
cd frontend && npm install && npm run dev   # http://localhost:5173
```

## API

| 端点 | 说明 |
| --- | --- |
| `POST /v1/recommendations` | 统一推荐入口。body `{"route_id","recent_route_ids"?}`，返回 `strategy`/`model_version`/`request_id`/最多 5 条 `{route_id,title,source}`；当前路线不存在 404，候选不足按实际数量返回 |
| `GET /v1/items/{route_id}/recommendations` | 无历史快捷入口（供压测/浏览器直接用） |
| `GET /v1/routes?city_id=&tag=&q=&size=` | 路线筛选列表；`q` 为统一搜索（标题全文 + 标签精确 + city_id 通配三路命中） |
| `GET /v1/routes/{route_id}` | 单条路线详情 |
| `POST /v1/track` | 埋点，`{"events":[{type,route_id,request_id,strategy,position}]}`，写 Redis `events` 列表 |
| `GET /metrics` | `es_recalls` / `cache_hits` / `rules_fallbacks` 计数 |
| `GET /healthz` | 健康检查 |

```bash
curl -s http://localhost:8080/v1/recommendations -H 'Content-Type: application/json' \
  -d '{"route_id":"penang-hill-funicular","recent_route_ids":["ipoh-oldtown-white-coffee"]}'
```

响应头：`X-Strategy: two_tower|rules`、`X-ES-Recall: 1|0`（是否触达 ES）。

## 前端

页面：首页（Hero 大搜索 + "想去哪里玩？"城市卡片墙）→ 城市页（路线列表 + 城内标签筛选）→ 详情页（面包屑 + 相似路线推荐，标注策略徽章）。搜索有独立结果页；标签点击一律新标签页打开；顶栏常驻搜索框。

- dev：`npm run dev`（`/v1` 代理到 :8080，无 CORS）
- 埋点：推荐返回批量上报 impression、点击推荐上报 click，带 `request_id`/`strategy`/坑位
- 构建：`npm run build` → `frontend/dist`，静态托管 + 反代 `/v1` 即可（HashRouter 无需服务端路由回退）

## 数据与接入新地区

路线 JSON：`{route_id, title, country_code, city_id, tags, image_url}`。`city_id` 用国家命名空间（`MY:george-town`、`CN:shanghai`），tags 是可跨地区复用的英文标识。

**接入新地区四步**（以清迈为例）：

```bash
# 1. 准备 data/th_chiang-mai.json（route_id 全局唯一，标签尽量复用现有词表）
# 2. 导入 + 生成封面 + 重建向量（顺序不能乱：import 会冲掉向量，embed 放最后）
go run ./cmd/import -file data/th_chiang-mai.json
CF_ACCOUNT_ID=... CF_API_TOKEN=... python tools/gen_images.py routes   # 只生成缺图的
go run ./cmd/embed
```

- `tools/gen_images.py`：读 `data/*.json`，按稳定 ID 生成 1024×576 WebP → `frontend/public/images/{routes,cities}/` → 回填 `image_url` 与城市清单；**已生成的自动跳过**，429/额度错误保留进度退出，重跑续跑；提示词在脚本 `HINTS` / `CITY_HINTS` 表里按 route_id 增补
- 产物来源记录：`data/image-gen.json`（每条含完整 prompt、模型、时间戳），全部为 AI 生成图，无第三方版权问题

## 双塔模型（model/）

- 结构：标签 Embedding 均值池化 → 各塔投影 32 维 L2 归一化；查询塔 = 当前路线 ⊕ 历史平均（无历史仅用当前路线），物品塔只看标签（新路线免训练可编码）
- 样本：`data/sessions.json` 34 组会话，按完整会话 80/20 划分（seed=42），每正样本 4 负样本（排除正/当前/已看）
- 未知标签：编码时跳过；全无已知标签 → 离线 embed 跳过、在线 422 回退规则
- 版本：`model/artifacts/v1/`（model.pt + vocab.json + meta.json），Python 服务、`MODEL_VERSION`、ES `vector_model` 三处一致
- 评估：`python model/evaluate.py`（留出测试集 Recall@5 对比 + ANN vs 精确点积重合率，README 下方有实测数字）

## 缓存与回退

- 键：`rec:v2:tower:{model}:{catalog}:{route_id}:{历史指纹}` 与 `rec:v2:rules:{route_id}:{历史指纹}` 两个命名空间**相互隔离**
- `catalog:version` 由 embed 命令 INCR；换模型/导数据后旧双塔缓存自动失效
- Python 独立短超时（`PYTHON_TIMEOUT` 默认 500ms），失败或 ES kNN 失败回退规则（ES 布尔召回 + 本地排序），规则结果同样缓存

## 图片与版权说明

- 路线/城市封面：Cloudflare Workers AI（FLUX.1 Schnell）生成的插画，风格统一；提示词与生成记录见 `tools/gen_images.py` 和 `data/image-gen.json`
- **封面图不随仓库分发**（`.gitignore` 排除 `frontend/public/images/`）；克隆后运行 `python tools/gen_images.py routes && python tools/gen_images.py cities`（需 CF 凭证）即可重建，或把数据里的 `image_url` 置空，前端自动回退到渐变占位
- 代码可自由用于学习与演示；数据为虚构的旅行路线示例

## 测试与验证

```bash
go test ./...              # 5 个包：规则排序 / API 流程 / ES 查询构造 / 推理客户端 / Redis
python model/evaluate.py   # 留出集上 规则 vs 双塔 Recall@5、ANN vs 精确重合率
```

实测（2026-11-12）：
- `go vet` 无告警，`go test ./...` 全部通过（含 request_id 归因、统一搜索查询结构、埋点、缓存隔离等用例）
- 训练：GPU（RTX 5060，torch 2.11+cu128），81 训练样本 / 18 测试样本
- 评估：规则 Recall@5 = 1.000，双塔 = 0.167（模拟会话按同城连续浏览编写，天然偏向规则；样本量小有过拟合），ANN 与精确点积 Top5 平均重合 0.978
- 压测（30 VUs）：热缓存 0 次 ES 召回、错误率 0%；K8s 双副本压测中删除 Pod，0 失败
- 内容：62 条路线 / 12 城，封面 52 张全部 AI 生成并经人工目验

## Docker / Kubernetes / Render

```bash
docker compose -f deploy/docker-compose.yml up -d   # ES + Redis（+ 可选 CPU 版 model 服务）
docker build -t route-recommender:latest -f deploy/Dockerfile .
docker build -t route-recommender-model:latest -f deploy/Dockerfile.model .
kind load docker-image route-recommender:latest route-recommender-model:latest
kubectl apply -f deploy/k8s.yaml                    # Go 2 副本 + 模型 1 副本
```

本地 K8s 访问宿主机依赖：kind 用 `host.docker.internal`（清单默认）；minikube 全局替换为 `host.minikube.internal`。

**Render 部署要点**：ES/Redis 用 Render 托管或同网络服务，`ES_URL`/`REDIS_ADDR` 指过去；模型产物打进镜像或挂盘且 `MODEL_VERSION` 三处一致；前端 `dist` 做 Static Site 并配 `/v1` 反写到 Go 服务。

## 环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `ADDR` / `ES_URL` / `ES_INDEX` | `:8080` / `http://localhost:9200` / `routes` | Go 监听与 ES |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` | `localhost:6379` / 空 / `0` | Redis |
| `MODEL_URL` / `MODEL_VERSION` | `http://localhost:8000` / `v1` | Python 推理服务与模型版本 |
| `CACHE_TTL` | `5m` | 推荐缓存时间 |
| `REDIS_TIMEOUT` / `ES_TIMEOUT` / `PYTHON_TIMEOUT` | `300ms` / `800ms` / `500ms` | 各下游子超时（均小于服务端 10s 总预算） |

## 代码中的中文注释导读

关键 Go 写法在首次出现处有 1–2 行注释：`cmd/server/main.go`（signal.NotifyContext、Shutdown、流量中间件、X-Forwarded-For）、`internal/api/handler.go`（r.Context 传递、withTimeout 的 cancel 为何 defer、依赖子超时与总预算、缓存键隔离、kNN 请求构造）、`internal/store/es.go`（defer Body.Close 位置、嵌套 JSON tag、统一搜索三路 should）、`internal/cache/redis.go`（ErrCacheMiss 哨兵、%w 保留错误识别）、`internal/route/route.go`（slices.SortFunc 比较器、多级排序键）、`internal/api/handler_test.go`（隐式接口、t.Run 子测试）。
