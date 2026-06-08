# polar-film 开发计划

> 配套设计稿:[design.md](./design.md)。产品定位:**视频/电影知识库(Video Knowledge Base)**——
> 不是视频网站,而是"豆瓣式元数据库 + AI 检索层":电影元数据、分段字幕、截图、人物、时间轴、
> 标签、多级向量。核心哲学:**存"知识结构"而非视频文件**(存储省几个数量级,AI 价值更高)。
> 本文把 design.md 对齐到 **Polar 平台模块规范**(参照系 `polar-dns` / `polar-hosts`)。
> 状态:计划稿,待拍板第 8 节开放决策。

---

## 0. 定位与平台对齐(先纠偏)

| design.md 原案 | 平台规范(对齐后) | 原因 |
|---|---|---|
| 裸 SQL / `uuid` 主键 | **Gin 服务 + TEXT 前缀 id**(`mv_`/`sub_`/`seg_`/`sc_`/`pe_`) | 与 hosts/dns 一致 |
| 表无租户字段 | **每张表带 `workspace_id TEXT NOT NULL`** | 平台多租户硬约束 |
| `/api/movies` | **`/api/film/*`**(dock 反代 `/api/film/`→film-svc) | 平台路由前缀按模块名 |
| 自管 DB,直连 | **自有库 `polar_film`,只连自己**;跨域引用走 TEXT 指针 + SDK | database-ownership.md |
| 截图/海报存哪没定 | **二进制 → polar-assets**(本模块只存元数据 + 资产指针) | 正合"存知识不存视频";复用资产数据面 |
| 向量库 pgvector/Qdrant/… | **首选 pgvector**(就在 `polar_film` 库,零外部依赖) | 自包含;规模真大了再外接 Qdrant |
| AI 分析自己接模型 | **LLM 走 dock LLM 代理**(`/internal/v1/llm/chat-completion`);ASR/OCR/embedding 见 §4 | 复用平台 LLM + 计费 |
| 只做 Movie | **首切 Movie,schema 预留泛化到 `media_items`**(剧集/纪录片/讲座/播客…) | design 终局是 "GitHub for Video Metadata" |

**不变的核心**(design 已对):七层模型(Movie / Subtitle / Screenshot / People / Timeline / Tag / Embedding)、
分段字幕(台词级检索)、截图(以图搜片)、AI 分析流水线、向量检索。

---

## 1. 模块骨架

```
modules/polar-film/
├── cmd/film-svc/main.go            # env→Config→New→RegisterRoutes→Start(heartbeat)
├── internal/film/
│   ├── plugin.go  auth.go  config.go  metrics.go     # 平台接线(照 polar-dns)
│   ├── movies_handlers.go / movies_store.go          # 电影 CRUD
│   ├── people_handlers.go / people_store.go          # 人物 + movie_people
│   ├── subtitles_handlers.go / subtitles_store.go    # 字幕上传 + SRT 解析 → segments
│   ├── screenshots_handlers.go / screenshots_store.go# 截图(二进制→assets)
│   ├── tags_handlers.go / tags_store.go              # 标签 + movie_tags
│   ├── timeline_handlers.go / timeline_store.go      # 时间轴事件
│   ├── search_handlers.go                            # 关键词 + 向量检索
│   ├── analyze_handlers.go / analyze_jobs.go         # AI 分析异步任务
│   ├── assets_client.go                              # 调 polar-assets 存/取二进制
│   ├── embed.go                                      # 调嵌入模型,写 pgvector
│   └── internal_*.go                                 # /internal/v1/film/*(dock→plugin)
├── web/                            # 独立产品级前端(浏览/检索/时间轴/字幕),go:embed
├── scripts/migrate/film-schema.sql # 幂等建表(含 pgvector)
├── go.mod  Makefile  README.md  LICENSE(MIT)
```
默认端口 `127.0.0.1:8102`(dns 占 8101)。Go module:`github.com/networkextension/polar-film`。

---

## 2. 数据库设计(对齐后,库 `polar_film`,启用 `CREATE EXTENSION vector`)

每张用户表带 `workspace_id`;二进制不入库(存 polar-assets,表里只放 `asset_id`/`url`)。

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE media_items (              -- 首切只放电影,kind 预留泛化
    id TEXT PRIMARY KEY,                -- mv_<rand>
    workspace_id TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'movie', -- movie|episode|doc|lecture|podcast|...
    title TEXT NOT NULL, original_title TEXT, year INT, country TEXT, language TEXT,
    runtime_min INT, summary TEXT,
    poster_asset_id TEXT,               -- → polar-assets
    imdb_id TEXT, douban_id TEXT, tmdb_id TEXT,
    created_by TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, kind, title, year)
);
CREATE TABLE people (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    name TEXT NOT NULL, avatar_asset_id TEXT, bio TEXT,
    UNIQUE(workspace_id, name)
);
CREATE TABLE media_people (             -- 关系
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    role TEXT NOT NULL,                 -- actor|director|writer|...
    character TEXT, ord INT,
    PRIMARY KEY(media_id, person_id, role)
);
CREATE TABLE subtitles (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    lang TEXT NOT NULL, format TEXT NOT NULL DEFAULT 'srt',
    source TEXT,                        -- uploaded|asr
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE subtitle_segments (        -- 台词级,检索主力
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    subtitle_id TEXT NOT NULL REFERENCES subtitles(id) ON DELETE CASCADE,
    media_id TEXT NOT NULL,
    idx INT, start_ms INT NOT NULL, end_ms INT NOT NULL, text TEXT NOT NULL,
    embedding vector(1024)              -- 维度按所选模型
);
CREATE TABLE screenshots (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    ts_ms INT, asset_id TEXT NOT NULL,  -- 图在 polar-assets
    phash TEXT, ocr_text TEXT, embedding vector(1024)
);
CREATE TABLE media_timeline (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    start_ms INT, end_ms INT, event_type TEXT, description TEXT
);
CREATE TABLE tags (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    name TEXT NOT NULL, kind TEXT DEFAULT 'genre',  -- genre|theme|ai
    UNIQUE(workspace_id, name)
);
CREATE TABLE media_tags (
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    source TEXT DEFAULT 'manual',       -- manual|ai
    PRIMARY KEY(media_id, tag_id)
);
CREATE TABLE media_embeddings (         -- 电影级
    media_id TEXT PRIMARY KEY REFERENCES media_items(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL, embedding vector(1024)
);
-- 向量索引(ivfflat/hnsw)在数据量起来后再建。
```

---

## 3. API(`/api/film/*`,全部经 `requireAuthViaDock` + workspace 门禁)

| 方法 | 路径 | 说明 |
|---|---|---|
| POST/GET | `/api/film/movies`、`GET /movies/:id` | 电影 CRUD |
| PATCH/DELETE | `/api/film/movies/:id` | 改/删 |
| POST | `/movies/:id/subtitles` | 上传 SRT/VTT → 解析成 segments |
| GET | `/movies/:id/subtitles` `/segments` | 取字幕/分段 |
| POST | `/movies/:id/screenshots` | multipart 上传(图存 assets) |
| POST/GET | `/movies/:id/people`、`/tags`、`/timeline` | 关联人物/标签/时间轴 |
| POST | `/movies/:id/analyze` | 触发 AI 分析异步任务 |
| GET | `/api/film/search?q=台词&kind=subtitle|movie|screenshot` | 关键词 + 向量检索 |
| GET | `/api/film/people/:id` | 人物详情 + 参演 |

---

## 4. AI 分析流水线(异步任务,`analyze_jobs.go`)

`POST /movies/:id/analyze` 入队,后台 goroutine 串:
```
(无字幕→ASR)  →  字幕分段 embedding  →  截图 OCR + phash + embedding
          →  LLM 出 summary / tags / timeline(走 dock LLM 代理)  →  电影级 embedding
```
- **LLM**:dock `/internal/v1/llm/chat-completion`(复用平台模型 + 计费)。
- **Embedding**:需要一个嵌入模型——选项见 §8(平台是否有 embedding 端点 / 自部署 bge/m3 / 调外部)。先把 `embed.go` 抽象成接口,后端可换。
- **ASR**:whisper(自部署或 API);**OCR**:截图硬字幕/画面文字(PaddleOCR/Tesseract/视觉模型)。**ASR/OCR 重,后置到 M5**,且做成可选(没有就跳过,纯靠已上传字幕)。
- 任务状态写 `analyze_jobs` 表(queued/running/done/failed + 进度),避免重复跑。

---

## 5. 平台集成触点

1. **env**:`POLAR_FILM_DB_DSN`、`POLAR_DOCK_BASE`、`POLAR_PLUGIN_NAME=film`、`POLAR_PLUGIN_TOKEN`(缺失 fatal)、`POLAR_FILM_LISTEN=127.0.0.1:8102`、`POLAR_FILM_PUBLIC_BASE_URL`、`POLAR_FILM_METRICS_TOKEN`、`POLAR_ASSETS_BASE`(+ assets 的 HMAC/token)、嵌入模型配置。
2. **鉴权 + heartbeat**:照 polar-dns(`Dock.AuthVerifyWS` + `WorkspacePluginAccess` + 60s heartbeat 带 `PublicBaseURL`/UIRoute `/`)。
3. **polar-assets**:`assets_client.go` 上传截图/海报/头像,拿 `asset_id`;读时给签名 URL。需对齐 assets 的内部 API(待看 polar-assets 契约)。
4. **workspace 删除级联**:`/internal/v1/film/workspace-deleted` → 删本库该 ws 数据 + 通知 assets 清理(或留 GC)。

---

## 6. 里程碑(每个可独立 PR)

| # | 里程碑 | 出口 |
|---|---|---|
| **M0** | 骨架 + 平台接线 | 编译;healthz;连库(含 vector 扩展);ping dock;heartbeat;`film-schema.sql` 建表 |
| **M1** | 电影 + 人物 + 标签 CRUD | 豆瓣式核心元数据可增删改查;海报/头像走 assets |
| **M2** | 字幕上传 + 分段 | SRT/VTT 解析成 `subtitle_segments`;关键词搜台词 |
| **M3** | 截图 | multipart 上传→assets;phash;时间戳关联 |
| **M4** | 向量检索(pgvector) | 字幕/电影级 embedding + `search` 语义检索("搜台词"/相似片) |
| **M5** | AI 分析流水线 | analyze 异步任务:LLM tags/timeline/summary;(可选)ASR/OCR |
| **M6** | 独立 UI(`web/`) | 浏览/检索/时间轴/字幕查看,go:embed,tab 进入 |
| **M7** | 泛化 media_items + 硬化 | 接入剧集/纪录片/播客;向量索引;retries/metrics/部署文档 |

**MVP = M0–M2**(可用的电影元数据 + 分段字幕库);M3–M4 是差异化(以图搜/语义检索);M5 是 AI 价值放大。

---

## 7. 复用 / 参照
- 平台骨架/鉴权/heartbeat/凭据加密:`modules/polar-dns`(最近、最干净)、`polar-hosts`。
- 二进制存储:`modules/polar-assets`(刚 clone,需读其内部 API 契约)。
- LLM 调用:dock `/internal/v1/llm/chat-completion`(internal-api-v1.md)。
- 平台规则:`doc/arch/{open-platform,internal-api-v1,database-ownership,auth-and-tokens}.md`。

---

## 8. 决策记录

**已定(2026-06-07):**
1. **首切范围**:`media_items` + `kind=movie`(预留泛化,免迁表)。
2. **向量库**:**pgvector**(在 `polar_film` 库内,零外部依赖)。
4. **二进制**:截图/海报/头像 → **polar-assets**(本模块只存 `asset_id`)。
5. **ASR/OCR**:**后置 M5**;MVP 只吃已上传字幕。
6. **UI**:**独立产品级 `web/`**(go:embed,同 polar-dns)。

**已定(2026-06-07, M4):**
3. **嵌入模型 / 向量维度**:**ollama bge-m3,1024 维**(中文强、零 API key、零花钱)。后端经
   `Embedder` 接口抽象(`internal/film/embed.go`):OpenAI 兼容 `httpEmbedder` 通吃 ollama / DashScope /
   OpenAI,空 `POLAR_FILM_EMBED_BASE_URL` 时退化为确定性 `hashEmbedder`(rune-bigram,非语义,仅打通管线 + 测试)。
   维度定为 **1024**(bge-m3 / DashScope text-embedding-v3 / OpenAI text-embedding-3-* dims=1024 都对齐)。

**M4 dev 部署坑(记录,redeploy 必看):**
- **pgvector**:dev 是 **postgresql@16**,但 `brew install pgvector` 的 bottle 只含 pg17/18 → 必须**从源码编译**:
  `git clone --branch v0.8.2 pgvector && make install PG_CONFIG=/opt/homebrew/opt/postgresql@16/bin/pg_config`。
  `CREATE EXTENSION vector` 需 **superuser**(polar 角色不行)→ 先以默认超级用户建扩展,其余 ALTER/CREATE 以 polar 跑。
  ⚠️ 若 brew 升级/重装 postgresql@16,源码装的 `vector.dylib` 会被清掉,需重编。
- **ollama**:`brew install ollama`(0.30.6 bottle)**只带 MLX runner,缺 llama-server** → 跑 GGUF 的 bge-m3 报
  "llama-server binary not found"。改用**官方 tarball** `ollama-darwin.tgz`(自带全部 runner),复用已 pull 的
  `~/.ollama/models`(bge-m3 1.1G)。env:`POLAR_FILM_EMBED_BASE_URL=http://127.0.0.1:11434/v1`、`_MODEL=bge-m3`、`_DIM=1024`。
