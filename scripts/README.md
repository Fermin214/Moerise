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
| `check-models.mjs` | 设置热更新、模型诊断、Embedding 协议与失败响应 |
| `check-stars.mjs` | 既有真实向量、UMAP 快照、浏览器星图、API/绘制延迟、内存、增量与主动重建 |
| `check-map.mjs` | `check-stars.mjs` 的兼容入口，参数相同 |

除 Compose 脚本的可选浏览器模式外，其余联合验收都需要 `MOERISE_PLAYWRIGHT_MODULE`。若使用已安装的 Chrome，可设置 `MOERISE_PLAYWRIGHT_CHANNEL=chrome`。观星脚本需要 Linux、已运行的布局服务以及顺序对应的笔记 JSON 和 Float32 小端向量：

```bash
node scripts/acceptance/check-stars.mjs \
  --notes .local/corpus/notes.json \
  --vectors .local/corpus/vectors.f32 --dimensions 1024
```

脚本使用独立容器与测试账号。观星导入指定语料并复用已有向量；其他脚本使用虚构笔记。模型服务由本地替身提供，不需要真实模型凭据。报告和截图写入 `.local/` 下各自的随机目录。观星脚本失败时保留来源容器，使用相同语料参数加 `--resume .local/stars-acceptance-<id>` 可复用已导入的笔记、状态与索引。成功退出后清理来源容器，`--keep` 可保留它。Compose 脚本支持 `--help`；`--keep` 在成功后保留测试环境，`--skip-build` 复用已构建镜像。

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

`tests/testdata/search-quality.json` 定义检索问题标注，材料取自仓库中的公开文档。需要真实供应商的可选评测入口见[开发指南](../docs/DEVELOPMENT.md)。

---

## 四、Android 构建、联调与发行

Android 工程文件与生成产物的归属见[Android 文件与产物](../docs/ANDROID.md#文件与产物)。

GitHub Actions 运行 JVM 测试、Lint、APK 构建和真实 Core API 联调。`Main CI` 构建正式签名 APK，发布前按[真机检查清单](../docs/android/RELEASE-CHECKLIST.md)验证覆盖升级与交互。安装与升级见[安装说明](../docs/android/INSTALL.md)。

| 入口 | 用途 |
| --- | --- |
| `android/environment.sh`、`android/check.sh` | 配置工具链，运行 JVM 测试、Lint 和 Debug APK 构建 |
| `android/live-check.sh`、`android/stars-check.sh` | 对隔离 Core/Memos 运行 JVM API 或观星专项联调 |
| `android/start-fixture.sh` | 启动隔离服务，生成设备可用的连接信息 |
| `android/ci-scope.py`、`android/ci-pr-gate.py` | 按 Git 路径判定 Android `none` 或 `build`，汇总适用的 PR 检查 |
| `android/benchmark.sh` | 在 Linux 或 WSL2 的 USB 真机上采集 Profile，测量启动与观星 |
| `android/release.sh`、`android/verify-apk.py`、`android/licenses.py` | 发行构建、签名、16 KiB 对齐和第三方许可检查 |

CI 路径、联调策略和发布守护可在宿主机验证：

```bash
python3 -m pip install -r scripts/android/requirements.txt
python3 -B scripts/android/ci-scope.test.py
node --test scripts/android/fixture-policy.test.mjs
python3 -B -m unittest discover -s scripts/release -p 'test_*.py'
```
