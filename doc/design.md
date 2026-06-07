如果你的目标是：

> 用户上传一个视频（默认是电影），系统通过 API 提交字幕、电影信息、截图，最终形成类似豆瓣的元数据库（Metadata Database），供 AI、搜索、推荐、知识图谱使用。

那么建议不要一开始做成「视频网站」，而是做成 **Video Knowledge Base（视频知识库）**。

---

# 核心模型

```text
Movie
├── Metadata
├── Subtitle
├── Screenshots
├── People
├── Timeline
├── Tags
├── Reviews
└── Assets
```

---

# 第一层：Movie

电影主表

```sql
movies
```

| 字段             | 类型        |
| -------------- | --------- |
| id             | uuid      |
| title          | varchar   |
| original_title | varchar   |
| year           | int       |
| country        | varchar   |
| language       | varchar   |
| runtime        | int       |
| summary        | text      |
| poster_url     | text      |
| imdb_id        | varchar   |
| douban_id      | varchar   |
| tmdb_id        | varchar   |
| created_at     | timestamp |

---

例如：

```json
{
  "id":"movie_001",
  "title":"霸王别姬",
  "year":1993,
  "runtime":171
}
```

---

# 第二层：字幕

最重要的数据

```sql
subtitles
```

| 字段       | 类型    |
| -------- | ----- |
| id       | uuid  |
| movie_id | uuid  |
| lang     | zh-CN |
| format   | srt   |
| content  | text  |

---

进一步拆分

```sql
subtitle_segments
```

| 字段       |
| -------- |
| movie_id |
| start_ms |
| end_ms   |
| text     |

---

例子

```json
{
  "start":123000,
  "end":126000,
  "text":"说好了一辈子，少一年都不行。"
}
```

这样未来：

```sql
vector search
```

直接搜台词。

---

# 第三层：截图

很多人低估这个价值。

```sql
screenshots
```

| 字段        |
| --------- |
| movie_id  |
| timestamp |
| image_url |
| phash     |
| embedding |

---

例子

```json
{
  "timestamp":3600,
  "image":"xxx.jpg"
}
```

---

未来可以：

```text
以图搜电影
```

或者

```text
这个画面出自哪部电影
```

---

# 第四层：人物

```sql
people
```

| 字段     |
| ------ |
| id     |
| name   |
| avatar |
| bio    |

---

关系表

```sql
movie_people
```

| 字段        |
| --------- |
| movie_id  |
| person_id |
| role      |

---

例子

```json
{
  "person":"张国荣",
  "role":"actor"
}
```

或者

```json
{
  "person":"陈凯歌",
  "role":"director"
}
```

---

# 第五层：时间轴

这个是 AI 时代非常有价值的东西。

```sql
movie_timeline
```

| 字段          |
| ----------- |
| movie_id    |
| start       |
| end         |
| event_type  |
| description |

---

例如

```json
{
  "start":530000,
  "end":560000,
  "type":"fight",
  "description":"主角与反派首次冲突"
}
```

---

以后 AI 可以回答：

```text
高潮部分在哪？
```

```text
有哪些打斗场景？
```

---

# 第六层：Tag

标签系统

```sql
tags
```

```sql
movie_tags
```

---

例子

```text
爱情
战争
悬疑
赛博朋克
成长
悲剧
```

---

以及 AI 自动生成：

```text
父子关系
社会阶层
宿命论
宗教隐喻
```

---

# 第七层：Embedding

这是未来最重要的部分。

---

电影级

```sql
movie_embeddings
```

```json
{
  "movie_id":"x",
  "embedding":[...]
}
```

---

字幕级

```sql
subtitle_embeddings
```

```json
{
  "segment_id":"y",
  "embedding":[...]
}
```

---

截图级

```sql
screenshot_embeddings
```

```json
{
  "screenshot_id":"z",
  "embedding":[...]
}
```

---

存放：

* pgvector
* Qdrant
* Weaviate
* Milvus

均可。

---

# API 设计

## 创建电影

```http
POST /api/movies
```

```json
{
  "title":"霸王别姬",
  "year":1993
}
```

返回

```json
{
  "id":"movie_001"
}
```

---

## 上传字幕

```http
POST /api/movies/:id/subtitles
```

```json
{
  "language":"zh-CN",
  "srt":"..."
}
```

---

## 上传截图

```http
POST /api/movies/:id/screenshots
```

multipart

```text
1.jpg
2.jpg
3.jpg
```

---

## AI 分析

```http
POST /api/movies/:id/analyze
```

后台任务：

```text
OCR
↓
ASR
↓
LLM
↓
Embedding
↓
Tag
↓
Timeline
```

---

# 最终架构

```text
               ┌────────────┐
               │ Movie API  │
               └─────┬──────┘
                     │
       ┌─────────────┼─────────────┐
       │             │             │
       ▼             ▼             ▼

 Metadata      Subtitle DB     Screenshot DB

       │             │             │

       └─────────────┼─────────────┘
                     ▼

              AI Analyzer

                     ▼

        Timeline / Tags / Summary

                     ▼

               Vector DB
              (Qdrant)
```

如果是你正在做的 Polar / AI Native Cloud 方向，我会进一步扩展成 **Media Graph**：

```text
Movie
Episode
Anime
Documentary
Lecture
Podcast
Youtube
TikTok
```

统一一个 `media_items` 表，而不是只做电影。

这样未来任何视频内容都能接入：

```text
字幕
截图
Embedding
知识图谱
AI问答
```

最后演化成：

> GitHub for Video Metadata

上传视频不重要，上传视频的“知识结构”才重要。这样存储成本低几个数量级，但 AI 检索价值反而更高。

