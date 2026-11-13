# 参与 Moerise 贡献

欢迎使用中文或英文提交 Issue、Pull Request 与架构讨论。

报告问题时，说明操作步骤、预期行为、实际结果，以及 Moerise 和 Memos 版本。示例中去除凭据与私人笔记内容。

较大的功能提议先通过 Issue 讨论需求与设计；修复问题时提供能够复现与验证结果的步骤或测试。

---

## 开发环境

使用 Linux 或 Linux 容器、Go 1.27+、Node.js 24+ 与 npm。真实 Memos 集成测试需要 Docker；常规单元测试与 Web 构建无需 Docker、在线 Memos 或模型凭据。

```bash
# 下载 Go 依赖
go mod download

# 安装并构建前端
npm --prefix web ci
npm --prefix web run build

# 后端与前端单元测试
go test ./...
npm --prefix web test
```

### 从源码运行

1. 复制 `config.example.json` 到 `.local/config.json`。
2. 配置独立测试 Memos，将数据目录设为 `.local/state` 与 `.local/index`。
3. 启用 `web.enabled`，设置 `web.public_origin`，生成独立的 32 字节 Base64 状态加密密钥。
4. 启动 Core 与 Web：

```bash
go run . check-config
go run . serve --host 127.0.0.1
npm --prefix web run build
npm --prefix web start
```

Core 默认监听 8787，Web 默认监听 3000。Web 的 `PUBLIC_ORIGIN` 必须匹配 Core 的 `web.public_origin`。`CORE_ORIGIN` 指定固定上游，默认 `http://127.0.0.1:8787`。Web 提供 `web/dist` 产物，源码修改后需重新构建。完整流程见[开发指南](docs/DEVELOPMENT.md)。

---

## 代码结构

完整模块分组与阅读顺序见[代码导览](docs/CODEBASE.md)。

| 路径 | 职责 |
| --- | --- |
| `main.go`、`internal/` | 命令行、共享服务、HTTP API 与平台适配 |
| `internal/openapi.json` | 内嵌 OpenAPI v3 契约 |
| `internal/migrations/` | 状态库与索引库的版本化 SQLite 迁移 |
| `web/src/` | 无第三方运行依赖的 JavaScript、HTML 与 CSS 前端 |
| `web/server.mjs`、`web/build.mjs` | 静态服务、同源代理与构建 |
| `web/test/` | Node 单元测试与 Playwright 浏览器测试 |
| `tests/` | 真实 Memos 集成测试 |
| `scripts/` | 联合验收、性能评测与共用工具，见[脚本说明](scripts/README.md) |
| `docs/` | 产品、架构、接口与运维文档 |

### 开发约定

1. **Memos 权威来源**：笔记、标签、附件与身份只通过官方 API 访问，所有操作遵守绑定作者范围。
2. **双库边界**：`state.sqlite3` 保存需要备份的设置、聊天与回顾等产品状态；`index.sqlite3` 必须可以从 Memos 重建。
3. **数据库迁移**：结构变化新增编号 SQL 文件，保持已应用迁移不变。
4. **外部只读**：机器 REST/MCP 严格只读；笔记写入由本人会话客户端与内部专用记录服务提供。

---

## 验证改动

按改动范围选择已有检查：

```bash
go vet ./...
gofmt -l main.go internal tests
go test ./...
npm --prefix web run build
npm --prefix web test

# 浏览器端到端流程
cd web && npx playwright install chromium && npm run test:browser
```

同步、Memos 协议与备份恢复变化，在 Docker 可用时运行：

```bash
go test -tags=integration ./tests -v -count=1
```

---

## 界面国际化

Web 支持简体中文与英语，翻译层位于 `web/src/i18n.js`：

- 中文源文案作为消息键，`web/src/en.js` 提供英语对应文案。
- 缺失翻译回退到中文源文案。
- 浏览器语言未匹配中文时使用英文。

### 界面翻译规范

- 使用 `l('中文文案', { name: value })`，通过 `setText` 或 `setAttr` 绑定；使用完整文案与占位符，让译文能够调整语序。
- 静态元素标注 `data-i18n`，输入提示与辅助名称使用 `data-i18n-placeholder`、`data-i18n-aria-label` 等属性。
- 避免动态拼接消息；保留图标、快捷键与可访问性信息。
- 仅翻译控件和系统提示，笔记正文、标签、文件名、自定义 Prompt 与历史聊天保持原样。

## 开源协议

贡献内容遵循项目的 [MIT License](LICENSE)。
