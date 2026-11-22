# Moerise 开发脚本

所有命令从仓库根目录运行。脚本按联合验收、性能评测和共用工具分类；基础测试入口见[开发指南](../docs/DEVELOPMENT.md)。

---

## 一、联合验收

`acceptance/` 将真实 Memos、Core、Web 与浏览器串起来，验证跨服务行为。需要 Docker Engine、Compose 2.24+、Go 1.27+、Node.js 24+ 以及已安装的 Playwright 浏览器。

```bash
npm --prefix web ci
npm --prefix web exec -- playwright install chromium
export MOERISE_PLAYWRIGHT_MODULE="$PWD/web/node_modules/playwright/index.mjs"
node scripts/acceptance/check-compose.mjs --browser
```

| 脚本 | 覆盖内容 |
| --- | --- |
| `check-telemetry.mjs <接收端二进制>` | 在隔离目录启动真实接收端，验证 HTTP 入库与周报 CSV，退出后清理数据库 |
| `check-compose.mjs` | Compose 构建、启动、健康检查、代理、会话、观星开关与可选布局服务、持久化与备份恢复；`--browser` 增加浏览器流程 |
| `check-discovery.mjs` | Web 每日回顾、语义漫步、只读机器接口和模型异常 |
| `check-chat.mjs` | 材料选择、流式回复、追问、保存成笔记与重启恢复。`--android-insights <目录>` 使用 Android 设备验收的 `insights/test-data.json` 和同目录的 `result.json`，比对两端选择的原文和写入 Memos 的笔记。 |
| `check-models.mjs` | 设置热更新、模型诊断、多种 LLM/Embedding 协议与失败响应 |
| `check-stars.mjs` | 既有真实向量、UMAP 快照、浏览器星图、API/绘制延迟、内存、增量与主动重建 |
| `check-map.mjs` | `check-stars.mjs` 的兼容入口，参数相同 |

除 Compose 脚本的可选浏览器模式外，其余联合验收都需要 `MOERISE_PLAYWRIGHT_MODULE`。若使用已安装的 Chrome，可设置 `MOERISE_PLAYWRIGHT_CHANNEL=chrome`。观星脚本需要 Linux、已运行的布局服务以及顺序对应的笔记 JSON 和 Float32 小端向量：

```bash
node scripts/acceptance/check-stars.mjs \
  --notes .local/corpus/notes.json \
  --vectors .local/corpus/vectors.f32 --dimensions 1024
```

脚本使用独立容器与测试账号。观星导入指定语料并复用已有向量；其他脚本使用虚构笔记。模型服务由本地替身提供，不需要真实模型凭据。报告和截图写入 `.local/` 下各自的随机目录。观星脚本失败时保留来源容器，使用相同语料参数加 `--resume .local/stars-acceptance-<id>` 可复用已导入的笔记、状态与索引。成功退出后清理来源容器，`--keep` 可保留它。Compose 脚本支持 `--help`；`--keep` 在成功后保留测试环境，`--skip-build` 复用已构建镜像。

---

## 二、性能与检索质量

`performance/benchmark-search.py` 在临时数据库上生成一万条、1024 维的合成数据，测量索引构建、冷/热向量扫描和完整检索服务。

```bash
python3 scripts/performance/benchmark-search.py --cpu 2 --output .local/search-2c.json
python3 scripts/performance/benchmark-search.py --cpu 4 --output .local/search-4c.json
```

Linux 可直接使用本机 Go；其他环境可通过 `--docker-image` 指定 Linux Go 镜像。`--cpu` 控制测量进程的 `GOMAXPROCS`，不等同于主机 CPU 限额。需要模拟实际容器限额时，通过 Docker 设置 CPU 和内存。`--phase` 可单独选择测量阶段，完整参数见 `--help`。

`performance/evaluate-search.py` 对已运行的 Core 执行带答案标注的问题集，输出 Hit@5、MRR@5 和查询延迟。

```bash
python3 scripts/performance/evaluate-search.py \
  --config .local/config.json --credential reader \
  --questions .local/search-questions.json \
  --output .local/search-evaluation.json
```

Core 需完成同步；语义检索还需启用并完成向量构建。问题文件为 JSON 数组，每项包含 `query`、`expected`，可附 `state` 和 `tags`。`expected` 保存预期笔记资源名数组。两个脚本都使用新的输出文件名。

---

## 三、共用工具与数据

`lib/process.mjs` 提供带超时、取消和输出上限的子进程执行；测试检查退出、超时和取消后的清理。

```bash
node --test scripts/lib/*.test.mjs
```

`tests/testdata/search-quality.json` 定义检索问题标注，材料取自仓库中的公开文档；`chat-quality.json` 提供虚构的聊天评测材料。需要真实供应商的可选评测入口见[开发指南](../docs/DEVELOPMENT.md)。

---

## 四、Android Windows 工具

Android 工程文件与生成产物的归属见[Android 文件与产物](../docs/ANDROID.md#文件与产物)。

GitHub Actions 的 Android 检查通过 `android/ci-device-check.sh` 启动隔离 Core/Memos，
复用设备用例验证登录、编辑、草稿和离线恢复。`Main CI` 构建签名 APK，检查最低系统版本
的安装和应用流程。`Publish release` 将通过检查的 APK 和校验文件发布到对应版本。

设备与构建入口见[设备检查](../docs/android/DEVICE-CHECKS.md)，发行与升级见
[安装说明](../docs/android/INSTALL.md)。Windows 使用 PowerShell 7.2 以上和 JDK 17。
所有 worktree 的 Gradle、安装、设备测试、Profile 和发行构建通过
`android/with-device-lock.ps1` 串行执行，源码固定为 `C:\Projects\Moerise`。
共享锁为 `C:\Projects\Moerise\.local\android\device-build.lock`，结果放在主仓库
`.local/android/` 的独立任务目录。只读 `-Status` 显示持有者、源码提交和残留构建进程。

| 入口 | 用途 |
| --- | --- |
| `environment.ps1` | 解析 JDK、共享 SDK/Gradle 缓存及 Go/Docker CLI；`-ResolveOnly` 只定义解析函数 |
| `build.ps1` | 串行 Gradle 任务、统一 JVM 参数和构建日志；支持 `-Offline`、`-ValidateOnly` |
| `phone.ps1` | 持久真机会话：目标 APK 复用、Maestro 默认回归、按需专项、人工接管与显式清理 |
| `phone-session.py` | fixture 会话与 `accept --release` 正式包真机入口；设备/APK 绑定、连续模块、补测、统一报告和设备恢复，详见 [Maestro](android/maestro/README.md#windows-正式包真机入口) |
| `maestro_runner.py`、`maestro-run.py` | 共用 UI 执行、独立测试数据、服务端后验、JUnit 与脱敏产物 |
| `maestro/` | 登录、编辑、搜索、归档删除、洞察、导航、附件和语言流程及静态校验 |
| `acceptance.ps1` | 动态设备选择、隔离包安装、登录/笔记/输入法；`-Recovery` 增加离线和后台自动同步；`-SkipBuild` 复用 APK |
| `ci-device-check.sh` | 隔离模拟器按 `MOERISE_ANDROID_SUITE` 执行 Maestro 界面组或 instrumentation 专项 |
| `fixture-artifacts.py` | 记录 Android 源码和两个 fixture APK 的摘要；界面组用 `verify --target-only` 单独校验目标包 |
| `device-recovery.py` | 分阶段 instrumentation、SQLite/WAL 落盘确认、杀进程重开及官方 API 无重复检查 |
| `start-fixture.ps1` | 隐藏进程启动隔离 Core/Memos/模型；`-Resume` 复用语料、端口与索引 |
| `fixture-control.ps1` | 单实例状态、保留数据停止、精确身份旧进程停止和显式销毁 |
| `benchmark.ps1` | 同一真机的 Profile、启动与观星性能；发现模拟器即报错 |
| `benchmark-device.py` | 显式安装性能 APK、运行 instrumentation 并拉取原始结果，供两平台入口共用 |
| `trace-processor.py` | 为性能 runner 下载固定版本及摘要的官方 Perfetto 程序，输出到构建目录 |
| `release.ps1` | 发行、签名、APK 对齐与许可归档；`-SigningFile` 读取仓库外签名档案 |
| `usb-https.mjs` | 本机 TLS 与限定域名 CONNECT 代理，证书和私钥作为文件路径传入 |

锁与模型限流的专项检查无需 Gradle、ADB、Docker 或设备：

```powershell
pwsh -NoProfile -File scripts/android/device-lock.test.ps1
node --test scripts/android/fixture-policy.test.mjs
python -B scripts/android/phone-session.test.py
python -B scripts/android/maestro-runner.test.py
python -B scripts/android/maestro/validate.py
python -B scripts/android/maestro/validate_test.py
python -B scripts/android/device-recovery.test.py
```

这些宿主检查覆盖跨进程互斥、残留文件、PID 重用、令牌继承、模型排队、
会话复用、安装取消恢复、代理只读检查和 SQLite/WAL 失败分类，不连接手机。
Android 行为使用上述设备流程集中验证。
