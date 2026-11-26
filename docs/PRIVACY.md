# 使用与故障统计

首次欢迎页和「设置 → 隐私」提供「分享使用与故障统计」开关，默认关闭。仅当用户主动开启、登录本人实例且实例允许转发时，客户端才开始计数。Web 按浏览器和站点保存选择，Android 按安装保存选择；升级沿用既有选择。关闭后立即停止收集并清除未发计数，更换账号或实例也会清空未发计数；开启前的操作不会补传。

## 字段与用途

维护者使用这些汇总计数了解常用功能、故障比例和等待时间分布。每批仅包含协议版本、平台、客户端版本、Core 版本、浏览器或系统大版本及分组计数，无法可靠获取的值填 `unknown`。完整枚举见[版本 1 字段清单](../internal/telemetry/schema-v1.json)。

```json
{"schema":1,"platform":"web","client_version":"0.3.0","core_version":"0.3.0","system_major":"unknown","rows":[{"operation":"save_ack","result":"success","duration":"lt1s","source":"user","count":2}]}
```

| 操作 | 计数口径 |
| --- | --- |
| 笔记创建、编辑、同步 | `enqueue` 记录入队，`save_ack` 记录远端确认结果；同一次写入及其自动重试只计一次。Android 的 `sync` 仅记录用户触发、有效变更、新冲突和非成功结果；不记录空轮询成功或既有冲突。 |
| 搜索、回顾、漫步、观星、启动 | 区分用户发起、预取与后台触发；耗时取自编辑器就绪、首条结果、星图首读。 |
| 日期、标签、逐条选材 | 仅记录提交成功、超限、取消或其他结果；笔记资源名、日期范围、标签与查询词留在设备与本人 Core。 |
| 客户端、请求、崩溃与 ANR | 仅记录固定类别与次数；已发出但结果无法确认记 `unknown`。 |

耗时分档为 `<300ms`、`300ms–1s`、`1s–3s`、`3s–10s`、`>=10s` 与 `unknown`。成功率分母仅包含有明确结果的样本，`unknown` 单独列出。周报仅统计自愿开启设备的操作次数，不能推算用户数或留存率。

## 接收端与保留期限

客户端最多缓存 48 小时计数，每 24 小时在前台最多向本人 Core 发送一批。Core 通过编译时注入的 HTTPS 地址转发，超时或失败直接丢弃。协议不包含设备或账号标识、IP、Cookie、Token、请求 ID、服务地址、笔记正文、对话、标签、查询词、附件、异常原文或堆栈。

官方发行构建的默认接收地址为 `https://telemetry.hifermin.com/v1/batches`。接收服务及入口代理均关闭来源 IP、请求头与请求体访问日志。接收端按 UTC 日期累加，仅保留含当天在内最近 30 个 UTC 日期的汇总，并在启动和每天 UTC 零点清理过期记录。隐私相关问题可通过[仓库 Issues](https://github.com/Fermin214/Moerise/issues)联系。

## 诊断摘要与反馈

「设置 → 隐私」支持随时预览、删减并复制本机诊断摘要（即使关闭统计开关也可使用）。摘要仅含操作、时间、版本、错误码、耗时档和请求 ID，便于自托管用户对照服务端日志排查。打开 [GitHub Issues](https://github.com/Fermin214/Moerise/issues) 不会自动附带摘要、截图或日志。

## 部署者开关与自建接收端

- **关闭转发**：官方构建默认允许 Core 转发已授权设备的汇总，部署者可通过 `MOERISE_TELEMETRY_FORWARDING=false` 或配置文件 `telemetry_forwarding: false` 关闭。源码构建未指定接收地址时不会外发。
- **自建接收端**：独立程序 `moerise-telemetry` 仅依赖单个 SQLite 文件（默认监听 `127.0.0.1:8091`，systemd 单元示例见 `deploy/telemetry/moerise-telemetry.service`），Core、Web 和 Android 均无需运行该程序：

```bash
go build -o moerise-telemetry ./cmd/moerise-telemetry
./moerise-telemetry -db /var/lib/moerise-telemetry/daily.sqlite3 serve
./moerise-telemetry -db /var/lib/moerise-telemetry/daily.sqlite3 report
```

通过 `-ldflags "-X moerise/internal.telemetryReceiverURL=<HTTPS 地址>"`（镜像使用构建参数 `PROJECT_TELEMETRY_RECEIVER`）即可指向自建接收端；请在前置 HTTPS 代理关闭访问日志与请求体采样。
