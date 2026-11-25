# Moerise 开发指南

本指南面向 Moerise 代码贡献者，提供本地开发环境配置、测试套件执行、端到端集成验收及评测流程说明。

源码阅读顺序与模块职责见[代码导览](CODEBASE.md)；跨服务验收和性能工具见[开发脚本](../scripts/README.md)。

---

## 一、环境准备

- **Go 1.27.4+**：服务端编译、运行测试与静态检查。构建无需 C 编译器（`modernc.org/sqlite` 纯 Go 驱动）；运行 `go test -race` 需 C 工具链。
- **Node.js 24+ 与 npm**：构建和测试 Web 前端。
- **Docker Engine 与 Compose 2.24+**：运行真实 Memos 容器进行集成端到端验收。日常单元测试无需 Docker。
- **Git**：版本控制。
- **Python 3.12**：原生运行布局服务；使用布局容器时无需在宿主安装科学计算依赖。

---

## 二、本地开发起步

### 1. 依赖管理
```bash
go mod download
go mod verify
```

### 2. 本地 Core 服务调试
1. 复制配置文件模板：`cp config.example.json .local/config.json`；
2. 在 `.local/config.json` 中配置测试用 Memos 地址、PAT、绑定作者及独立的 `access_tokens`、`cursor_secret`；
3. 将 `state_dir` 和 `index_dir` 分别设为 `.local/state` 和 `.local/index`；
4. 启用 `web.enabled`，将 `web.public_origin` 设为 `http://localhost:3000`，用 `openssl rand -base64 32` 生成独立的 `web.state_encryption_key`；
5. 校验并启动核心服务：
   ```bash
   go run . check-config
   go run . serve --host 127.0.0.1
   ```

### 3. Web 前端开发与热运行
先构建网页，再编译或启动 Core：
```bash
npm --prefix web ci
npm --prefix web run build
go run . serve --host 127.0.0.1 --port 3000 --web-dir web/dist
```

访问 `http://localhost:3000`。`--web-dir` 在每次请求时读取磁盘文件；修改源码后重新执行 Web 构建即可刷新。发布构建不传此参数，网页由二进制内嵌提供。

### 4. 观星布局服务

```bash
python3.12 -m venv .local/layout-venv
.local/layout-venv/bin/pip install -r services/layout/requirements.txt
MOERISE_LAYOUT_STATE="$PWD/.local/layout-data" \
  .local/layout-venv/bin/python services/layout/server.py serve
```

Core 默认连接 `127.0.0.1:8766`，可通过 `MOERISE_LAYOUT_URL` 修改。也可运行 `docker compose up -d --build layout`，将 Core 放入同一 Compose 网络。算法、快照与模型文件说明见[观星](STARGAZING.md)。

---

## 三、代码检查与测试套件

### 1. 基础代码分析与单元测试
日常提交前必须通过以下检查：
```bash
# 代码格式化检查（无输出即为通过）
gofmt -l main.go internal tests

# Go 静态分析
go vet ./...

# Go 后端单元测试
go test ./...

# 前端构建与测试
npm --prefix web run build
npm --prefix web test
```

### 2. 内存竞争检测 (Race Detector)
在具备 C 工具链的环境中执行：
```bash
go test -race ./...
```

---

## 四、Playwright 浏览器自动化测试

发往 `main` 的 PR 按变更路径运行检查。Go 或 Layout 改动并行运行构建与单元测试、竞态检测和 Memos 集成测试。Web 或共用脚本改动运行 Web 检查。Android 客户端、Core 或 Web 代理改动还会运行 Android 检查。同一 PR 推送新提交会取消旧检查，`PR gate` 要求新提交的全部适用检查和发布预演通过。可按 PR 编号或运行 ID 查询失败步骤、耗时和产物，见 [CI 排查技能](../.agents/skills/moerise-ci/SKILL.md)。

Moerise Web 前端使用 Playwright 覆盖真实的端到端浏览器交互：

```bash
# 安装 Chromium 浏览器内核
cd web
npx playwright install chromium

# 执行完整浏览器测试套件
npm run test:browser
```

浏览器缺少系统库时，按 Playwright 提示安装依赖。CI 通过 `MOERISE_PLAYWRIGHT_CHANNEL=chrome` 使用 Runner 预装 Chrome，并通过 `MOERISE_PLAYWRIGHT_MODULE` 指定 Playwright 模块路径。本地安装 Chromium 后可直接运行测试。

### 专项浏览器测试说明
- `web/test/browser.mjs`：基础页面交互、Markdown 渲染、就地编辑与多维筛选。
- `web/test/settings-browser.mjs`：动态设置保存、凭据加密、版本冲突与时区热更新。
- `web/test/discovery-browser.mjs`：每日回顾全天计划浏览、随机漫步路径步进与中断重试。
- `web/test/map-browser.mjs`：旧快照迁移渲染、鼠标与键盘操作、节点点选阅读；正式星图与真实向量的联合流程见 `scripts/acceptance/check-stars.mjs`。
- `web/test/insights-browser.mjs`：AI 洞察材料多选、SSE 流式打字机回复、追问与一键沉淀。

---

## 五、真实 Memos 集成测试

当涉及 Memos 协议同步、数据库迁移、备份恢复或通知推送改动时，必须通过真实容器集成测试：

观星集成场景要求布局服务已在 `MOERISE_LAYOUT_URL` 指定的地址运行。

```bash
go test -tags=integration ./tests -v -count=1
```

测试流程会自动拉取 Memos 0.31.x 官方镜像，启动临时独立容器并完成初始化注册、PAT 签发、双向数据读写与断流容灾校验，并在测试结束后自动清理容器与匿名数据卷。

需要同时验证 Compose 部署、Web 代理和真实浏览器时，使用[联合验收脚本](../scripts/README.md#一联合验收)。

### 可选的真实供应商测试

以下测试均需 Docker 和独立测试配置，普通 CI 默认跳过。配置文件放在 `.local/`，报告使用新的输出路径。

| 测试 | 必需环境变量 | 行为 |
| --- | --- | --- |
| `TestSearchQualityLiveAcceptance` | `MOERISE_SEARCH_QUALITY_CONFIG`、`MOERISE_SEARCH_QUALITY_OUTPUT` | Linux 下以仓库文档为材料调用真实 Embedding，测量检索质量、延迟与资源占用；可能产生模型费用 |
| `TestChatQualityLiveAcceptance` | `MOERISE_CHAT_QUALITY_CONFIG`、`MOERISE_CHAT_QUALITY_OUTPUT` | 将虚构材料发送给真实 LLM，保存回复与协议检查结果，供人工评价；可能产生模型费用 |
| `TestFeishuLiveReviewAcceptance` | `MOERISE_FEISHU_LIVE_CONFIG` | 向配置的真实飞书接收方发送虚构笔记与图片；可用 `MOERISE_FEISHU_LIVE_WEBHOOK` 提供含 `url`、`secret` 的 JSON 文件，追加群 Webhook 测试 |
| `TestFourLiveBotDestinations` | `MOERISE_BOTS_LIVE_CONFIG` | 在独立 Memos 中核对三种私聊的笔记、回执和回顾，按真实时钟依次向四种目的地投递 |

四渠道测试配置为 JSON。`channel` 使用配置模板中的字段，填写专用 QQ、Telegram、飞书应用凭据和飞书群 Webhook。`timezone` 填实例时区。首次接入时，在约 8 分钟内向三个机器人各发一句带 `#验收` 的笔记，再发 `/回顾`。Telegram 启动链接保存在配置文件旁的私有临时文件中，按日志给出的路径打开链接并点「开始」。

已有私聊地址时，可在顶层填写 `qq_peer`（该 QQ 机器人的本人 OpenID）和 `telegram_private_chat_id`（该 Telegram 机器人的本人私聊 ID，使用字符串）。填写 `qq_peer` 后，程序先发一条 QQ 定时回顾作为通知，收到后有约 20 分钟发送三种私聊消息。填写 `telegram_private_chat_id` 后可直接在原私聊发消息。用 `first` 可指定第一个时段，格式为带时区偏移的 RFC 3339 整分时间。四个目的地在同一天验收，运行时设置 `-timeout=1h`。

要额外验证 QQ 在当天没有私聊消息且回复窗口已过时能主动推送，可在顶层设置 `quiet_day_qq` 为 `true`，并使用 `-timeout=36h`。发送测试消息后保持 QQ 私聊安静，程序会在次日同一时刻检查平台回执。测试结束后自动清理临时链接、Docker 实例和笔记。

单独排查私聊收发时，用 `-run '^TestFourLiveBotDestinations$/^private$'`。`first` 可以设晚一些，为人工发消息预留时间。需要在多次联调中复用同一 Telegram 启动链接时，在私有配置里设置固定的随机 `cursor_secret`。

设置对应环境变量后，按测试名选择执行：

```bash
go test -tags=integration ./tests -run '^TestSearchQualityLiveAcceptance$' -v -count=1 -timeout=30m
```

检索评测读取 `tests/testdata/search-quality.json` 的来源列表和问题标注，并记录各篇材料的 SHA-256；修改文档标题时需同步标注引用。聊天数据集位于 `tests/testdata/chat-quality.json`。

---

## 六、基准测试与检索质量评测

### 1. 向量扫描与观星联合验收
```bash
# 向量点积与内存扫描 Benchmark
go test ./internal -run '^$' -bench 'Benchmark(VectorIndex|Semantic)' -benchmem
```

观星联合验收使用既有笔记和对应的 Float32 小端向量，不调用真实模型：

```bash
node scripts/acceptance/check-stars.mjs \
  --notes .local/corpus/notes.json \
  --vectors .local/corpus/vectors.f32 --dimensions 1024
```

脚本记录快照 API 延迟、Core 内存、首次星图显示时间和连续缩放绘制耗时，同时检查新增笔记的坐标稳定性与主动重建。

### 2. 检索效果评测脚本
提供标准评测脚本 `scripts/performance/evaluate-search.py`，用于对标注问题集评测 Hit@5、MRR@5 及查询延迟：

```bash
python3 scripts/performance/evaluate-search.py --config .local/config.json --credential reader --questions .local/search-questions.json
```

先运行已完成同步与向量构建的 Core，在配置中提供名为 `reader`、含 `notes:read` 的访问凭据。问题文件为 JSON 数组，每项包含 `query` 与预期资源名数组 `expected`，可附 `state` 和 `tags`；输出路径默认 `.local/search-evaluation.json`，需使用新的文件名。向量扫描基准只测计算过程，完整 API 延迟还包含来源认证、模型请求与原文复查。

---

## 七、开发与提交规范

1. **接口规范同步**：当修改 `internal/models.go` 或 `internal/client_models.go` 或路由时，同步更新 `internal/openapi_gen.go` 生成规则以及 [API 文档](API-V1.md)。
2. **数据库迁移规范**：涉及 SQLite 数据表结构变动时，必须在 `internal/migrations/` 中新增编号递增的 SQL 迁移文件，**严禁修改已发布的旧迁移文件**。
3. **敏感信息隔离**：测试用例、脚本与文档中严禁硬编码真实的私有密钥、生产 Token 或包含用户私人数据的笔记原文。

## 发布

PR 按改动范围复用发布流程。Linux 预演在 AMD64 和 ARM64 各自构建安装包和 Core、Layout 镜像，检查镜像能启动并提供服务。Android 预演使用临时密钥，在最低支持版本 Android 10（API 29）安装签名 APK，通过 HTTPS 登录，写入并读回笔记，重启后检查会话。Android 设备回归在 API 36 上覆盖完整编辑、附件和洞察。

合并后，`Main CI` 并行运行两个 Linux 架构任务和一个 Android 任务。Linux 任务保存安装包和镜像摘要。Android 任务使用生产密钥，对同一 APK 检查清单、签名、16 KiB 对齐及最低版本设备流程，并记录设备验证所用 APK 的 SHA-256。

准备联合发版时，通过一个 PR 对齐 Core、Web 和 Android 的版本号，同时更新 OpenAPI、npm 锁文件、Compose 和 `.env.example` 中的版本。Core 与 Web 的版本以 `internal/config.go` 和 `web/package.json` 为准。Android 在 `mobile/androidApp/build.gradle.kts` 中更新 `versionName` 并递增 `versionCode`。

在 Actions 启动 `Publish release`，填写发布提交对应的成功 `Main CI` 运行 ID。默认的 `both` 先核对两个平台的版本，再用同一次运行的产物发布一个 `v<版本>` Release。附件包含 Linux AMD64 和 ARM64 安装包、签名 APK、APK 的 SHA-256 和构建信息、Android 安装说明和许可归档。Core、Layout 的同版本镜像使用该次运行记录的摘要。

单独发布 Core/Web 时选择 `core-web`，标签为 `v<版本>`。单独发布 Android 时选择 `android`，标签为 `android-v<版本>`。标签已指向其他提交或已公开的同标签附件不同，工作流会终止。工作流产物保留 90 天，或以仓库设置允许的期限为准。
