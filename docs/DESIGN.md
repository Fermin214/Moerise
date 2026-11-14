# Moerise Web 集成与系统设计

本文档阐述 Moerise Web 客户端的系统集成设计、前后端通信契约以及核心交互模块的技术实现方案。

---

## 一、集成方案与系统拓扑

Moerise 采用 **Memos + Moerise Core + Moerise Web** 的三层架构，保证轻量解耦与最小化运维复杂度：

```mermaid
flowchart LR
    Browser["用户浏览器"] -->|"同源 HTTPS / Cookie"| Web["Moerise Web (前端服务: 3000)"]
    Web -->|"内部反向代理 (/api/app/v1)"| Core["Moerise Core (核心服务: 8787)"]
    Core -->|"官方 Connect / REST API"| Memos["官方 Memos (底座: 5231)"]
    Core --> State[("state.sqlite3 (业务状态)")]
    Core --> Index[("index.sqlite3 (可重建索引)")]
```

### 关键设计决定

1. **统一单域名与同源反向代理**：
   - 浏览器仅访问 Moerise Web 暴露的对外服务地址（如 `https://notes.example.com`），无需跨域。
   - Web 服务端内置轻量同源反向代理，将 `/api/app/v1/*` 路径转发至内部 Core 服务，静态资源与 HTML 由 Web 直接分发。
2. **多层鉴权凭据隔离**：
   - 浏览器持有 Moerise 颁发的不透明会话 Cookie（`HttpOnly`、`SameSite=Lax`、`Secure`）。
   - Memos 长期管理员 PAT 仅保存在 Core 服务端，不暴露给前端浏览器。
   - 外部自动化与 AI 工具通过独立的只读 Bearer Token 访问 Core 的 `/api/ext/v1` 和 `/mcp`。
3. **前端无庞大第三方框架依赖**：
   - Web 前端采用原生现代 JavaScript、HTML5 与原生 CSS 构建，无运行时依赖打包，加载极快、资源占用极低。

---

## 二、通信契约与安全机制

### 1. 认证与会话生命周期

- **Memos 身份认证适配**：
  - 用户在 Web 登录页输入 Memos 用户名和密码。
  - Core 调用 Memos 官方认证协议验证凭据，获取访问令牌与刷新 Cookie，并校验账号是否属于实例绑定的作者。
  - 校验通过后，Core 在 `state.sqlite3` 中加密记录持久化会话，并通过响应头向浏览器设置会话 Cookie。
- **Cookie 安全策略**：
  - 生产 HTTPS 环境使用前缀安全 Cookie：`__Host-moerise_session`；本地回环调试使用 `moerise_local_session`。
  - 设置 `HttpOnly` 防止 XSS 窃取，设置 `SameSite=Lax` 防止跨站伪造，设置 `Path=/`。
- **CSRF 双重防御**：
  - 登录成功后，Core 向前端返回唯一的 `csrf_token`，仅保存在浏览器运行内存中。
  - 登录请求校验 `Origin`；登录后的非只读请求（POST / PATCH / DELETE）还需提供 `X-CSRF-Token`，且 `Origin` 必须精确匹配配置的 `web.public_origin`。

### 2. 请求幂等性设计

- **保存防重复**：
  - 创建笔记必须附带客户端生成的 `Idempotency-Key`（UUID 或唯一随机串）。
  - Core 将该幂等键与请求摘要保存在 `state.sqlite3` 中。
  - 当遭遇弱网超时或连接异常中断时，用户点击重试将复用相同幂等键，确保 Memos 中绝不产生重复条目。

---

## 三、前端核心模块设计

### 1. 布局与双语国际化

- **响应式布局**：
  - 侧栏和边距随窗口调整，正文限制阅读宽度，地图使用完整工作区。回顾与漫步固定阅读框，长文在框内滚动。
  - 视口不超过 960px 时使用手机工具栏与导航抽屉，支持边缘滑动、搜索展开和屏幕安全区。
  - 切换布局保留筛选、输入焦点与笔记草稿；导航抽屉使用原生模态 `dialog` 管理焦点。
- **双语自适应与热切换**：
  - 采用中文词条作为 Message Key，通过 `i18n.js` 进行客户端映射。
  - 按浏览器语言顺序匹配，登录页和设置支持手动切换或恢复跟随浏览器，偏好仅保存在本机。
  - 原地更新界面文案，保留表单、草稿、地图视口与对话状态。
- 设置按模块使用顶部横向页签，高级参数直接展示，切换页签保留输入并统一保存。
- 回顾与漫步使用卡片动画，遵守 `prefers-reduced-motion`；漫步预览和关联结果均来自 Core 接口。

### 2. 时间线与卡片即时编辑

- **实时时间线加载**：
  - 默认以 `created_desc` 倒序展示未归档笔记。
   - 点击「加载更多」继续读取分页（基于 `page_token` 游标机制）。
  - 搜索支持关键词、语义和混合方式，与标签、日期筛选组合使用；筛选栏仅展示标签和日期，清除筛选保留搜索词与搜索方式。
- **卡片内即时编辑**：
   - 从卡片的「更多操作」菜单进入编辑，支持 `Ctrl+Enter` 快速保存、`Esc` 取消。
  - 编辑只修改 Memos 中的正文或标签，创建时间不变，不影响时间线排序。

### 3. 2D 认知地图

- Core 在后台生成点位、语义主题、子主题与关联的每日快照，页面读取不触发模型计算；无文本向量的笔记独立展示。
- 地图铺满工作区，支持主题聚焦、节点联动、平移缩放与键盘操作，遵守减少动态效果设置。
- 全景展示主题色块，放大后按可用面积展开子主题分区和笔记地块；边界几何复用，摘要按需读取。
- 预览与详情读取 Memos 实时原文；桌面侧边展示详情，窄窗口使用底部面板。

### 4. 流式 AI 洞察

- **灵活选材**：
  - 支持多选任意历史笔记作为会话的上下文材料。
  - 严格限制笔记正文字符规模（默认 20,000 Unicode 码点以内），防止超出模型上下文或产生高额费用。
- **Server-Sent Events (SSE) 流式传输**：
  - 客户端通过 POST 请求发起会话，并通过 SSE 协议实时接收大模型 token 流。
   - 支持主动停止生成与持久会话；重启后保留中断文本，不自动续调模型。
  - 满意的 AI 产出支持一键幂等保存为 Memos 笔记。

---

## 四、运维与容器编排拓扑

仓库根目录的 [compose.yaml](../compose.yaml) 定义完整编排；以下为服务关系摘要：

```yaml
services:
  memos:
    profiles: [memos, local]
    image: neosmemo/memos:0.31.0
    restart: unless-stopped
    volumes:
      - memos-data:/var/opt/memos

  moerise:
    build: .
    restart: unless-stopped
    volumes:
      - state-data:/data/state
      - index-data:/data/index
      - backup-data:/backups
    environment:
      - MOERISE_MEMOS_BASE_URL=http://memos:5230
      - MOERISE_WEB_ENABLED=true

  web:
    build: ./web
    restart: unless-stopped
    depends_on:
      moerise:
        condition: service_healthy
    environment:
      - CORE_ORIGIN=http://moerise:8787
      - PUBLIC_ORIGIN=${MOERISE_PUBLIC_ORIGIN}
    ports:
      - "127.0.0.1:3000:3000"
```

Web 容器作为非 root 用户运行，仅对外暴露 3000 端口，前端和核心服务之间通过 Docker 内部网络安全通信。
