# Moerise 开发指南

本指南面向 Moerise 代码贡献者，提供本地开发环境配置、测试套件执行、端到端集成验收及评测流程说明。

---

## 一、环境准备

- **Go 1.27+**：服务端编译、运行测试与静态检查。构建无需 C 编译器（`modernc.org/sqlite` 纯 Go 驱动）；运行 `go test -race` 需 C 工具链。
- **Node.js 24+ 与 npm**：构建和测试 Web 前端。
- **Docker Engine 与 Compose 2.24+**：运行真实 Memos 容器进行集成端到端验收。日常单元测试无需 Docker。
- **Git**：版本控制。

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
4. 校验并启动核心服务：
   ```bash
   go run . check-config
   go run . serve --host 127.0.0.1
   ```

### 3. Web 前端开发与热运行
在另一个终端启动 Web 静态代理服务：
```bash
npm --prefix web ci
npm --prefix web run build
CORE_ORIGIN=http://127.0.0.1:8787 PUBLIC_ORIGIN=http://localhost:3000 npm --prefix web start
```

访问 `http://localhost:3000` 即可开始联调。修改前端源码后需重新执行 `npm --prefix web run build`。

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

Moerise Web 前端使用 Playwright 覆盖真实的端到端浏览器交互：

```bash
# 安装 Chromium 浏览器内核
cd web && npx playwright install --with-deps chromium

# 执行完整浏览器测试套件
npm run test:browser
```

### 专项浏览器测试说明
- `web/test/browser.mjs`：基础页面交互、Markdown 渲染、就地编辑与多维筛选。
- `web/test/settings-browser.mjs`：动态设置保存、凭据加密、版本冲突与时区热更新。
- `web/test/discovery-browser.mjs`：每日回顾全天计划浏览、随机漫步路径步进与中断重试。
- `web/test/map-browser.mjs`：2D 认知地图画布渲染、鼠标与键盘快捷键交互、节点点选穿透阅读。
- `web/test/insights-browser.mjs`：AI 洞察材料多选、SSE 流式打字机回复、追问与一键沉淀。

---

## 五、真实 Memos 集成测试

当涉及 Memos 协议同步、数据库迁移、备份恢复或通知推送改动时，必须通过真实容器集成测试：

```bash
go test -tags=integration ./tests -v -count=1
```

测试流程会自动拉取 Memos 0.31.x 官方镜像，启动临时独立容器并完成初始化注册、PAT 签发、双向数据读写与断流容灾校验，并在测试结束后自动清理容器与匿名数据卷。

---

## 六、基准测试与检索质量评测

### 1. 向量扫描与布局计算性能基准
```bash
# 向量点积与内存扫描 Benchmark
go test ./internal -run '^$' -bench '^BenchmarkSemantic' -benchmem

# 2D 认知地图 PCA 布局生成 Benchmark
go test ./internal -run '^$' -bench '^BenchmarkMapLayout$' -benchmem
```

### 2. 检索效果评测脚本
提供标准评测脚本 `scripts/evaluate-search.py`，用于对标注问题集评测 Hit@5、MRR@5 及查询延迟：
```bash
python3 scripts/evaluate-search.py --questions .local/search-questions.json
```

---

## 七、开发与提交规范

1. **接口规范同步**：当修改 `internal/models.go` 或 `internal/client_models.go` 时，必须同步更新 `internal/openapi.json` 以及 [API 文档](API-V1.md)。
2. **数据库迁移规范**：涉及 SQLite 数据表结构变动时，必须在 `internal/migrations/` 中新增编号递增的 SQL 迁移文件，**严禁修改已发布的旧迁移文件**。
3. **敏感信息隔离**：测试用例、脚本与文档中严禁硬编码真实的私有密钥、生产 Token 或包含用户私人数据的笔记原文。
