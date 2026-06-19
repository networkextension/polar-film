# 人脸/人物识别 + 编辑器级人工校正 (face curation)

## 目标
给剪辑师一套"按人物找镜头"的工具。自动聚类不可靠,所以**核心是人工校正**:
- 把被拆开的同一人**合并** (merge)
- 把聚类里混进的**错脸移除** (remove similar/wrong faces)
- 把两个人被并到一起的 cluster **拆分** (split)
- **命名** cluster → person

校正之后,剪辑师能**按人物快速定位所有镜头 + 台词**,并导出时间码 / EDL markers 进 NLE。

## 现状 (关键约束)
- **filmscan 本地**:Vision `VNDetectFaceRectangles` 检测 → `VNGenerateImageFeaturePrint` 贪心聚类
  (阈值 18.0,`Stages/Faces.swift`)→ `faces.json` = `[{frameIdx, timeMs, box, cluster}]`,
  **不持久化 embedding**。`Stages/Fuse.swift` 另聚 speaker(spkN,位置+特征双约束,阈值 24/0.15)
  并写 `speakers/spkN.jpg` 代表图。`Commands/Label.swift` 命名(手动 `--set` / TMDB feature-print 匹配)。
- **服务端无任何人脸数据** —— `Commands/Push.swift` 只上传 SRT(含 `[Name]`/`[spkN]` 前缀)+ 关键帧;
  `faces.json` / `fused.json` / `speakers/*.jpg` / embedding 全部丢弃。
- 服务端:`people`(name/avatar_asset_id/bio)+ `media_people`(cast,attach/detach)+
  `subtitle_segments.speaker_key/person_id`(SRT 解析时按名字 resolve people)。
  people API **只有 create/list/attach/detach**,没有 rename/merge/delete。
- **锚点**:`speaker_key`(spkN)是 cluster ↔ 台词的纽带。命名/合并一个 cluster ⇒ 改写该
  speaker_key 的所有 `subtitle_segments.person_id`。
- embedding(`VNFeaturePrintObservation`)用完即弃,Linux 服务端跑不了 Vision → 服务端不能重算聚类。

## 设计

### 1. 数据模型 (新增服务端表, additive `IF NOT EXISTS`)
- **`face_clusters`**:`id, media_id, workspace_id, speaker_key, person_id NULL,
  rep_screenshot_id, rep_box, face_count, source(filmscan|manual), conf`。
- **`faces`**:`id, media_id, cluster_id, screenshot_id, ts_ms, box(x,y,w,h 归一化),
  quality, embedding BYTEA NULL`。每张脸**引用已上传的关键帧** + box;
  **缩略图前端用 canvas 从关键帧裁,不另存裁脸资产**(省存储)。
- **`face_merge_suggestions`**:`media_id, cluster_a, cluster_b, distance` —— filmscan push 时
  用内存里的 feature-print 预算的"疑似同人"对(服务端跑不了 Vision)。

### 2. 上传 (扩展 filmscan push)
新端点 `POST /api/film/movies/:id/faces`(JSON,幂等):上传 `faces.json`(boxes+cluster)+
每 cluster 的 speaker_key + rep(screenshot_id+box)+ inter-cluster 距离矩阵中阈值内的疑似同人对。
关键帧已在传,脸只需引用 `screenshot_id` + box。可选:把 archived 的 feature-print 作 embedding 存
(为 P4 过渡)。

### 3. 校正 API
- `GET  /movies/:id/face-clusters` → clusters(rep + count + person + 合并建议)
- `GET  /movies/:id/face-clusters/:cid/faces` → 该 cluster 所有脸(screenshot_id+box+ts)
- `POST /movies/:id/face-clusters/merge {into, from:[…]}` → 并脸 + 合 person + 改写 segment ✅合并
- `POST /movies/:id/face-clusters/:cid/faces/remove {face_ids}` → 错脸移到未分配(cluster -1)/删除 ✅移除
- `POST /movies/:id/face-clusters/:cid/split {face_ids}` → 选中脸拆成新 cluster
- `POST /movies/:id/face-clusters/:cid/assign {name|person_id}` → 命名 → resolve people → 回写
  `segment.person_id` + `media_people`
- 补齐 people CRUD:`PATCH /people/:id`(rename/avatar/bio)、`POST /people/merge`、`DELETE /people/:id`

所有写操作在**事务**内同步更新 `subtitle_segments`(speaker_key→person_id)+ `media_people`,SRT 不重传。

### 4. 校正 UI (嵌入 `internal/film/web/index.html`,纯 vanilla)
影片页新增「人物」面板:
- **人物墙**:每 cluster 一张代表脸(canvas 从签名关键帧裁)+ 名字/未命名 + 脸数。
- 点 cluster → 展开所有脸缩略图(多选)。操作:**命名 / 合并(选另一 cluster 或拖拽)/
  移除选中 / 拆分选中**。
- **「疑似同人」**建议行:一键合并。
- 复用已有签名 URL + IntersectionObserver 懒加载 + lightbox(点脸跳到那一帧)。

### 5. 剪辑师价值 (重点)
- **按人物导航**:点一个人 → 他出现的所有关键帧 + 台词(带时间码)→ 一键跳帧。
- **导出**:每人物出入点时间码 / **EDL / FCPXML markers**,直接进 Premiere/FCP。

## 关键决策
- 裁脸放前端(存 box+screenshot_id);不存上千张裁脸资产。
- 相似建议 v1 走**客户端预算**(filmscan 有 feature-print);服务端 Linux 无 Vision。
- v2 升级 **ArcFace → 512 维向量入 pgvector**(film 已有 pgvector)→ 服务端最近邻 + 跨片人脸搜索
  (即 P3b)。
- 传播以 `speaker_key` 为锚:校正只改 cluster↔person 映射 + segment.person_id,不动关键帧/台词。

## 分期 (Plane: PF P0–P4)
- **P0** 服务端 face 表 + filmscan 扩展 push 上传脸/cluster/疑似同人对。
- **P1** 校正 API(merge/remove/split/assign + people rename/merge/delete)+ 事务传播。
- **P2** 「人物」校正 UI(人物墙 + 多选 + 合并/移除/拆分/命名 + 疑似同人)。
- **P3** 按人物导航 + 时间码 / EDL 导出(剪辑师收益)。
- **P4 (stretch)** ArcFace + pgvector(更准 + 跨片搜索 + 服务端相似建议)。

## 验证
- P0:push 后 `GET /face-clusters` 返回 N 个 cluster,`/faces` 返回 box+screenshot_id;前端能裁出脸。
- P1:merge 两 cluster → 该两 speaker_key 的 segment.person_id 收敛到同一 person;remove/split 改 face_count。
- P2:浏览器加载「人物」面板,代表脸正确;合并/移除/拆分/命名实时生效。
- P3:点人物列出其全部帧+台词时间码;导出的 EDL 在 Premiere/FCP 能识别。
