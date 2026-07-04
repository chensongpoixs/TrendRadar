# 定时抓取任务漏抓问题分析报告

## 问题描述
配置为每天 8:00-23:00 每个整点抓取数据（`preset: "daytime_8_23"`），但实际运行中发现部分整点时间没有抓取数据。

## 问题根因

### 1. **防重复执行逻辑存在 BUG**

**位置**: `backend-go/internal/scheduler/scheduler.go:156-165`

```go
// runCrawlTask 运行抓取任务
func (s *Scheduler) runCrawlTask() {
	s.mutex.Lock()
	now := time.Now()
	taskKey := "crawl"

	// 检查是否刚运行过（防止重复执行）
	if lastRun, exists := s.lastRun[taskKey]; exists {
		if now.Sub(lastRun) < time.Hour {  // ⚠️ 问题在这里
			s.mutex.Unlock()
			logger.WithComponent("scheduler").Info("crawl task skipped, ran recently", zap.String("op", "runCrawlTask"))
			return
		}
	}
	s.lastRun[taskKey] = now
	s.mutex.Unlock()
	// ...
}
```

**问题分析**:
- 代码使用 `now.Sub(lastRun) < time.Hour` 来判断是否在 1 小时内运行过
- 但是，如果上一次任务执行时间较长（例如 AI 分析、邮件发送等耗时操作），可能导致：
  - 假设 10:00 整点触发任务，实际执行完成时间是 10:45
  - `lastRun` 被设置为 10:00（任务开始时间）
  - 11:00 整点触发时，`now.Sub(lastRun)` = 1小时，**不满足 < 1小时的条件**，正常执行
  - 但如果任务在 10:00:00.001 开始，11:00:00.000 触发，差值是 59分59.999秒，**满足 < 1小时**，被跳过！

### 2. **实际日志证据**

从日志中可以看到跳过的模式：

```
2026-05-13 10:00:00 - crawl task skipped, ran recently  ❌
2026-05-13 12:00:00 - crawl task skipped, ran recently  ❌
2026-05-13 14:00:00 - crawl task skipped, ran recently  ❌
2026-05-13 16:00:00 - crawl task skipped, ran recently  ❌
2026-05-13 18:00:00 - crawl task skipped, ran recently  ❌
2026-05-13 23:00:00 - crawl task skipped, ran recently  ❌
```

**规律**: 
- 被跳过的时间点都是在上一次成功执行后的 **1 小时整**
- 由于时间精度问题（毫秒级差异），导致 `now.Sub(lastRun)` 可能略小于 1 小时

### 3. **Cron 配置正确**

```go
case "daytime_8_23":
    // 每天 8:00–23:00 每个整点各一次（共 16 次）
    addJob("0 0 8-23 * * *")  // ✅ 配置正确
```

Cron 表达式 `"0 0 8-23 * * *"` 的含义：
- 秒: 0
- 分: 0  
- 时: 8-23（每天 8 点到 23 点）
- 日: *（每天）
- 月: *（每月）
- 周: *（每周）

配置本身是正确的，会在每天 8:00, 9:00, ..., 23:00 共 16 个整点触发。

## 解决方案

### 方案 1: 修改时间判断逻辑（推荐）

将判断条件从 `< time.Hour` 改为 `<= 55*time.Minute`，留出 5 分钟缓冲：

```go
// 检查是否刚运行过（防止重复执行）
if lastRun, exists := s.lastRun[taskKey]; exists {
	if now.Sub(lastRun) <= 55*time.Minute {  // 改为 55 分钟
		s.mutex.Unlock()
		logger.WithComponent("scheduler").Info("crawl task skipped, ran recently", zap.String("op", "runCrawlTask"))
		return
	}
}
```

**优点**: 
- 简单直接
- 保留防重复执行的保护机制
- 允许每小时正常执行

### 方案 2: 使用更精确的时间判断

记录任务完成时间而非开始时间：

```go
func (s *Scheduler) runCrawlTask() {
	s.mutex.Lock()
	now := time.Now()
	taskKey := "crawl"

	// 检查是否刚运行过（防止重复执行）
	if lastRun, exists := s.lastRun[taskKey]; exists {
		if now.Sub(lastRun) < 50*time.Minute {  // 50 分钟内不重复
			s.mutex.Unlock()
			logger.WithComponent("scheduler").Info("crawl task skipped, ran recently", zap.String("op", "runCrawlTask"))
			return
		}
	}
	s.mutex.Unlock()  // 先解锁，允许并发检查

	logger.WithComponent("scheduler").Info("running scheduled crawl", zap.String("op", "runCrawlTask"))
	if err := runCrawlAnalyzeAndNotify(); err != nil {
		logger.WithComponent("scheduler").Error("scheduled task failed", zap.Error(err), zap.String("op", "runCrawlTask"))
	}
	
	// 任务完成后更新时间
	s.mutex.Lock()
	s.lastRun[taskKey] = time.Now()
	s.mutex.Unlock()
}
```

### 方案 3: 移除防重复逻辑（不推荐）

如果确定 Cron 不会重复触发，可以完全移除这个检查：

```go
func (s *Scheduler) runCrawlTask() {
	logger.WithComponent("scheduler").Info("running scheduled crawl", zap.String("op", "runCrawlTask"))
	if err := runCrawlAnalyzeAndNotify(); err != nil {
		logger.WithComponent("scheduler").Error("scheduled task failed", zap.Error(err), zap.String("op", "runCrawlTask"))
	}
}
```

**风险**: 如果有其他地方手动调用 `RunNow()`，可能导致短时间内重复执行。

## 影响范围

- **受影响时间段**: 8:00-23:00 每天约有 6-8 个整点被跳过
- **数据完整性**: 部分时段的热点新闻未被抓取和分析
- **用户体验**: 历史数据查询时会发现某些小时的数据缺失

## 建议修复优先级

**高优先级** - 建议立即修复

理由：
1. 影响核心功能（定时数据抓取）
2. 导致数据不完整
3. 修复简单，风险低

## 测试验证

修复后建议验证：
1. 连续观察 24 小时，确认每个整点都正常执行
2. 检查日志中不再出现 "crawl task skipped, ran recently"
3. 验证数据库中每个小时都有对应的快照数据

---

**报告生成时间**: 2026-05-24  
**分析人员**: Kiro AI Assistant
