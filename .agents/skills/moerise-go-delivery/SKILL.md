---
name: moerise-go-delivery
description: 开发、重构或排查 Moerise 跨端接口、认证会话、同步、双库迁移回滚，或编写高并发 Go 与验收测试时使用。
---

从仓库根目录运行，优先用静态检查和聚焦测试完成垂直闭环，再放行全量套件。

## 跨端契约与调用链同步

- 改动 HTTP 路由、状态码、错误码、SSE 事件、分页游标或读取路径前，先用 `grep -rn` 盘点 `internal/`、`tests/`、`web/src/`、`web/test/`、`mobile/`、`scripts/acceptance/` 与 `scripts/android/seed-*.mjs` 的全量调用方。
- 修改 `internal/openapi_gen.go` 或请求/响应结构体后，确认所有非 `204` / `HEAD` 的成功状态码（包括 `206`）都声明了媒体类型与 `content` Schema，且 `[]byte` 字段生成 Base64 `string`/`byte` 而非整数数组。
- 本地直读投影生效后，联调和种子脚本必须通过 `/api/app/v1` 写入，避免直接写 Memos 造成 Core 索引不可见。

## 同族边界与状态机齐步收口

- 认证与同步淘汰：不仅在入口 `Identity` 校验处接入 `onAuthFailure`，同步分页、原文复查与结束校验的每一处 `UPSTREAM_AUTH` / `IDENTITY_MISMATCH` 返回都要淘汰会话缓存并取消活跃请求；并发登记与登出撤销使用代次原子校验。
- 快照恢复与跨版本回滚：`Restore` 与 `Rollback` 统一复用队列、机器人收件箱与时间屏障重置逻辑，确保快照重放不会重发通知或重收历史事件。
- 索引迁移 SQL：涉及 `json_each`、类型判断或结构重建的迁移，必须以包含普通文本、中文、数字字符串、重复标签与 `NULL` 的存量记录实测（用 `j.type` 而非对明文再执行 `json_type(j.value)`）。

## 小步闭环与并发测试防护

- 跨文件重构按单个能力模块推进并立即运行 `gofmt`、`go vet` 与聚焦单测，不在堆积几十处修改后才首次编译运行。
- 涉及通道阻塞、流式响应、模拟服务端或锁持有的测试，将释放逻辑（`close(ch)`、关闭服务器/连接）挂入 `t.Cleanup`，避免前置 `t.Fatal` 导致收尾 `Close()` 永久死锁。
- 迭代运行 `-race` 单测显式加 `-timeout=120s`（全量 `internal` 带 `-race` 使用 `-timeout=25m`），不依赖 Go 默认 10 分钟超时暴露挂起。
- 依赖 Git 历史提交或二进制回归的测试，必须拆分并默认同时执行无历史依赖的纯迁移/状态路径，避免只在完整仓库通过而在 CI `fetch-depth: 1` 浅克隆下失败。
