# 邮件去重设计说明

本文档说明定时任务中「热榜结果邮件」的去重行为、数据模型、调用顺序与可优化方向。实现代码位于 `internal/storage/email_fingerprint.go`、`internal/storage/email_sent_storage.go` 与 `internal/scheduler/scheduler.go`。

## 1. 目标与范围

- **目标**：同一条目曾在邮件中**成功推送**过后，后续调度再出现时**不再**进入邮件正文；若本批经去重后**无新内容**，**不发送**邮件（避免空邮或纯重复信息）。
- **与本地存库的关系**：`SaveNewsData` 等仍保存 AI 过滤后的**全量**热榜，供前端与历史查询；**仅邮件内容**经 `FilterNotYetEmailed` 筛选。
- **范围**：当前指纹表仅服务于调度侧邮件去重；RSS 等若未合入 `results` 的邮件正文，则不受该表约束（以代码为准）。

## 2. 数据模型

| 表名 | 说明 |
|------|------|
| `email_sent_fingerprints` | 已发送过邮件的条目指纹（去重用） |

| 字段 | 说明 |
|------|------|
| `id` | 自增主键 |
| `sig` | 指纹字符串（64 字符十六进制 = SHA256 输出），**唯一索引** |
| `first_sent_at` | 首次写入时间（`ON CONFLICT DO NOTHING` 时不会更新已存在行，表示**第一次**标记为已发邮件的时间） |

GORM 模型：`pkg/model/email_sent.go` → `EmailSentFingerprint`。  
迁移：`internal/core/database.go` 的 `AutoMigrate` 中注册。

## 3. 指纹规则（业界常见做法）

### 3.1 写入库的主指纹 `EmailItemSignature`

实现：`storage.EmailItemSignature` → `internal/storage/email_fingerprint.go`。

**有 URL（`URL` 优先，否则 `MobileURL`）**

1. 将链接做**规范化**后再 `SHA256` 十六进制（见下）。无法规范化的（相对路径、非 http(s) 等）则退化为与历史一致的 **legacy**：`SHA256(小写原始整串)`。  
2. **规范化**（与爬虫/内容去重常见策略对齐）：仅 `http`/`https`；`Host`、路径小写相关处理；`path.Clean`；去掉 `Fragment`；去掉常见**跟踪类 query**（如 `utm_*`、`gclid`、`fbclid`、`spm` 等）；对剩余 query **按 key 排序**、同 key 多值再排序，保证同一资源不同参数顺序仍得到相同指纹。

**无 URL**

- 主指纹：`platformID + "\x00" + normalizeTitle(Title)`，`normalizeTitle` 为 `strings.Fields` 合并空白（降低仅多空格差异导致的假新）。

### 3.2 查询时 `matchSigs`（兼容升级与双轨制）

查询「是否已发送」时，对每条新闻计算**多个候选指纹**（新版 + 旧版），**任一条在库中即视为已发**：

- **URL 类**：规范化指纹 + 历史「整串小写」指纹（避免升级后已发条目因算法升级被再次推送）。  
- **无 URL 类**：新版标题归一 + 旧版 `Trim` 标题各一（若不同）。

**写入**仍只记 **主指纹**（新版），历史 legacy 行随时间自然可并存；首次在新算法下发信后会新增规范化指纹一行。

**仍可能出现的边界**：

- 彻底不同的短链指向同一文、且无法从 URL 判断同一资源时，仍可能多次推送。  
- 有 URL 时标题怎么改都仍以 URL 为主键。  
- 无 URL 时标题实质性改写会产生新指纹。

## 4. 调度中的调用顺序

入口：`runCrawlAnalyzeAndNotify`（`internal/scheduler/scheduler.go`）。

1. 热榜抓取 → `applyAIFocusFilter` → `results`。  
2. 对各平台 `SaveNewsData`（全量、与邮件去重独立）。  
3. 若启用 RSS，拉取并 `SaveRSSData` 等。  
4. `emailResults, emailSkipped, err := storage.FilterNotYetEmailed(results)`。  
5. 若去重查询 **失败**：记录日志，**回退**为不筛重（`emailResults = results`，`emailSkipped = 0`），避免 DB 不可用时用户完全收不到信（可能重复，属可用性优先策略）。  
6. 组纯文本与 HTML 时，**邮件正文/摘要**仅使用 `emailResults`。  
7. 未开启通知或收件人为空：直接 return（不发邮件、不写指纹）。  
8. 若本批 `mailCount == 0`（去重后无新条）：不发送、不写指纹，打日志。  
9. 发送：优先 HTML，失败再纯文本。  
10. 仅当发送**最终成功**后调用 `storage.RecordEmailSent(emailResults)`；写库失败仅打日志，不将整任务记为失败。

**要点**：指纹**只应在邮件发送成功之后**持久化，避免「未发却记为已发」导致漏推。

## 5. 核心 API

### `FilterNotYetEmailed(results)`

- 平台 key **排序** 后展平，顺序稳定。  
- 为每条计算 **主指纹** 与 `matchSigs`；**批内**若主指纹已出现，视为重复（**先出现者保留**），计入 `emailSkipped`。  
- 将本批**所有** `matchSigs` 去重后，对库做 `sig IN (?)` 查询；单批过大时按 **500** 条分片。  
- 任一条目若 **已有历史指纹** 或 **批内重复** 则跳过。  
- `GetDB() == nil` 时**不去重**（原样返回 `results`，`skipped = 0`）。

### `RecordEmailSent(results)`

- 为每条待记录条目生成与过滤时**相同**的 `sig`，`CreateInBatches` 批量插入；  
- 使用 `ON CONFLICT (sig) DO NOTHING`，重复写入安全。

## 6. 已知风险与产品取舍

| 场景 | 可能现象 |
|------|----------|
| 发送成功但 `RecordEmailSent` 失败 | 下次会再次推送，出现**重复邮件**。 |
| 去重库查询失败 | 回退**全量发送**，可能重复。 |
| 指纹表无 TTL / 无清理 | 表持续增长；`IN` 已分片，单条 SQL 有上限。 |
| 多平台出现同一「规范化 URL」 | 批内**主指纹**去重后只保留先遍历到的平台条目（平台名排序决定先后）。 |

## 7. 后续优化建议（供迭代参考）

- **发信与落库一致性**：对「发送成功、写库失败」做监控或异步重试。  
- **生命周期 / 再推送策略**：可过期指纹或按主题冷却，避免长历史库无限膨胀与「永不再提」的僵化。  
- **降级策略可配置**：DB 失败时是「全量发」还是「本小时不发」可配置。  
- **跟踪参数表**：`isTrackingQueryKey` 可按业务白名单/黑名单继续扩展。  
- **Punycode/国际化域名**：依赖 `url.Parse` 行为，一般足够；若遇边缘案例可再加强。

## 8. 相关文件

- `internal/storage/email_fingerprint.go` — URL/标题规范化、主指纹与 `matchSigs`  
- `internal/storage/email_sent_storage.go` — 过滤、分片查库、落库  
- `internal/scheduler/scheduler.go` — 调度流程与发送后调用 `RecordEmailSent`  
- `pkg/model/email_sent.go` — 表模型  
- `internal/core/database.go` — 自动迁移

---

*若行为与实现不一致，以代码为准，并请同步更新本文档。*
