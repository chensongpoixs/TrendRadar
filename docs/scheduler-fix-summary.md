# 定时抓取任务修复说明

## 问题
配置为每天 8-23 点每小时抓取，但实际运行中约 40% 的整点被跳过。

## 根本原因
防重复执行逻辑使用 `now.Sub(lastRun) < time.Hour` 判断，由于时间精度问题（毫秒级差异），导致整点触发时被误判为"1小时内已运行"而跳过。

## 修复方案
将判断条件从 `< time.Hour` 改为 `<= 55*time.Minute`：

```go
// 修复前
if now.Sub(lastRun) < time.Hour {
    // 跳过
}

// 修复后  
if now.Sub(lastRun) <= 55*time.Minute {
    // 跳过
}
```

## 效果
- ✅ 保留防重复执行保护（55分钟内不重复）
- ✅ 允许每小时整点正常触发
- ✅ 确保 8-23 点共 16 个整点都能正常抓取数据

## 验证方法
观察日志，确认每个整点都显示 "running scheduled crawl" 而非 "crawl task skipped"。
