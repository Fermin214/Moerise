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
| `check-compose.mjs` | Compose 构建、启动、健康检查、代理、会话、持久化与备份恢复；`--browser` 增加浏览器流程 |
| `check-discovery.mjs` | Web 每日回顾、语义漫步、只读机器接口和模型异常 |
| `check-chat.mjs` | 材料选择、流式回复、追问、保存成笔记与重启恢复 |
| `check-models.mjs` | 设置热更新、模型诊断、多种 LLM/Embedding 协议与失败响应 |
| `check-map.mjs` | 真实点集、浏览器画布交互、快照刷新、来源变化与备份恢复 |

除 Compose 脚本的可选浏览器模式外，其余联合验收都需要 `MOERISE_PLAYWRIGHT_MODULE`。若使用已安装的 Chrome，可设置 `MOERISE_PLAYWRIGHT_CHANNEL=chrome`。地图脚本需要 Linux，`MOERISE_MAP_SCALE` 可设为 100 至 10000，默认 1000。

脚本使用独立容器、测试账号和虚构笔记。模型服务由本地替身提供，不需要真实模型凭据。报告和截图写入 `.local/` 下各自的随机目录，退出时清理所创建的服务和测试数据。Compose 脚本支持 `--help`；`--keep` 在成功后保留测试环境，`--skip-build` 复用已构建镜像。

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
