# 使用与故障统计

首次欢迎页和「设置 → 隐私」提供「分享使用与故障统计」开关。开关默认关闭。确认开启、登录本人实例并取得实例许可后，设备才开始计数。Web 按浏览器和站点保存选择，Android 按安装保存选择。关闭后立即停止收集与发送并清除积存计数。更换账号或实例也会清除积存计数。开启前发生的操作不会补传。

## 字段与用途

项目维护者用这些计数检查常用功能、故障与等待时间，决定应优先改善哪些操作。每批只有协议版本、平台、客户端版本、Core 版本、浏览器或系统大版本，以及分组计数。无法可靠取得的值填 `unknown`。下例表示同一组中两次笔记保存获得服务端确认。完整枚举见[版本 1 字段清单](../internal/telemetry/schema-v1.json)。

```json
{"schema":1,"platform":"web","client_version":"0.1.2","core_version":"0.1.2","system_major":"unknown","rows":[{"operation":"save_ack","result":"success","duration":"lt1s","source":"user","count":2}]}
```

| 操作 | 计数口径 |
| --- | --- |
| 笔记创建、编辑、同步 | `enqueue` 记录成功入队，`save_ack` 记录远端保存结果。一次操作及其自动重试共计一次。 |
| 搜索、回顾、漫步、观星、洞察、启动 | 区分用户发起、预取与后台操作。等待时间取自编辑器可输入、搜索首个结果、观星首次可读或洞察首段回复。 |
| 日期、标签、逐条选材 | 记录提交成功、超限、取消或其他结果。笔记资源名、日期范围、标签值与查询词留在设备和本人 Core。 |
| 单条和组合保存 | 单独回答计入 `save_answer`，多条组合计入 `save_multiple`，含提问的选择另计 `save_question`。 |
| 客户端、请求、崩溃与 ANR | 只记平台可观测的固定类别和次数。一次操作已发出且结果无法确认时记 `unknown`。 |

耗时分为小于 300 毫秒、300 毫秒至不足 1 秒、1 秒至不足 3 秒、3 秒至不足 10 秒、至少 10 秒及未知。成功率分母为有明确结果的样本，未知数量单列。周报按端、版本和触发方式分组，仅涵盖自愿分享的操作次数，不能推算用户数或留存率。

## 接收者与保留时间

客户端最多缓存 48 小时的分组计数，每 24 小时最多在前台发送一批到本人 Core。Core 用固定 HTTPS 地址转发，超时或失败后丢弃。协议排除设备和账号标识、IP、Cookie、Token、请求 ID、服务地址、笔记、对话、标签、查询词、附件、异常原文及堆栈。网络入口可以看到 Core 的出口 IP，项目接收端和代理须关闭含来源地址或请求体的日志。

官方发行构建使用项目维护的接收地址 `https://telemetry.hifermin.com/v1/batches`，由 Cloudflare Tunnel 转发至 Dallas 接收端。接收服务不记录来源 IP、请求头或请求体；Tunnel 不启用访问日志或请求体采样。隐私问题可通过[仓库 Issues](https://github.com/Fermin214/Moerise/issues)联系维护者。接收端按 UTC 日期直接累加，只保存含当天在内最近 30 个日期的汇总。维护者按周查看样本量和已知、未知结果。统计目的或数据敏感程度变化时，应更新协议和说明并重新征求选择。

## 个案反馈

反馈入口直接打开仓库 Issues。设备上的诊断摘要可先预览、删减再复制，开关关闭时也可使用。摘要可包含操作、时间、版本、稳定错误码、粗分耗时和请求 ID，便于部署者查找自有日志。打开链接不会附加摘要、截图或日志。公开 GitHub Issue 的内容会被其他人看到。[反馈入口](https://github.com/Fermin214/Moerise/issues)

## 维护者部署

独立程序 `moerise-telemetry` 只需一个 SQLite 文件。将其放在访问受限的私有目录，用禁用请求日志的 HTTPS 代理转发 `POST /v1/batches`，并关闭代理和托管平台的 IP、请求头、请求体及追踪采样。Core、Web 和 Android 无需运行该程序。接收端启动和每天 UTC 零点删除过期日汇总。备份、周报导出如需留存也应遵守相同的 30 天期限。

```sh
go build -o moerise-telemetry ./cmd/moerise-telemetry
./moerise-telemetry -db /var/lib/moerise-telemetry/daily.sqlite3 serve
./moerise-telemetry -db /var/lib/moerise-telemetry/daily.sqlite3 report
```

`serve` 默认只监听 `127.0.0.1:8091`。每周运行 `report`，输出含当天在内最近七个 UTC 日期的 CSV。已知结果不足 20 次的组只显示数量。启用外发的 Core 构建使用链接参数 `-X moerise/internal.telemetryReceiverURL=<HTTPS 接收地址>`，镜像使用构建参数 `PROJECT_TELEMETRY_RECEIVER`。Core 默认允许转发，实例部署者通过 `MOERISE_TELEMETRY_FORWARDING=false` 或配置文件中的 `telemetry_forwarding: false` 关闭。此实例开关不改变设备同意状态；Web 和 Android 都需用户显式开启，升级保留既有设备选择。

Dallas 的用户级服务监听 `127.0.0.1:18091`；CLI 默认端口仍为 `8091`。用户级 systemd 单元位于 `deploy/telemetry/moerise-telemetry.service`。二进制安装到 `~/.local/lib/moerise-telemetry/moerise-telemetry`，数据目录为 `~/.local/share/moerise-telemetry`；将单元安装至 `~/.config/systemd/user/` 后执行 `systemctl --user enable --now moerise-telemetry`。服务日志不记录来源地址、请求头或请求体。Cloudflare Tunnel 仅转发接收子域名至回环监听端口，入口关闭请求日志与追踪采样。

发行流水线通过仓库变量 `PROJECT_TELEMETRY_RECEIVER` 同时注入原生 Core 二进制与 Core 镜像。源码构建未指定该值时，接收地址为空，Core 不外发。
