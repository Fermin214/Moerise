# Moerise Web

Moerise Web 是独立构建和部署的浏览器客户端，使用 Memos 用户名和密码登录。页面提供会话恢复、退出、本人正常笔记的创建时间倒序时间线、组合筛选、完整笔记管理、附件及持久设置，支持 Ctrl+Enter 保存。

登录表单沿用 Memos。登录后的界面使用 Web 原型的原始样式、图标、编辑器和卡片结构：浅色两栏、260 px 侧栏、最大 760 px 内容区，窄屏布局沿用原型。正文按纯文本显示，保留空白、换行和原生 `#标签`。

## 构建与运行

需要 Node.js 24 或更高版本，无第三方运行依赖。在仓库根目录执行：

```sh
npm --prefix web run build
npm --prefix web start
```

`build.mjs` 将 `web/src/` 复制到 `web/dist/`，并将原型的静态行内样式编译为独立的 `layout.css`，兼容同源 CSP。服务只提供构建后的文件；修改源码后重新构建。Memos 标识资源的许可证见 `web/MEMOS-LICENSE`。

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `CORE_ORIGIN` | `http://127.0.0.1:8787` | Core 的固定 HTTP(S) 来源，仅服务端使用 |
| `PUBLIC_ORIGIN` | `http://localhost:3000` | 浏览器访问的完整来源，仅 HTTPS 或 loopback HTTP，写请求精确匹配 |
| `HOST` | `0.0.0.0` | Web 监听地址 |
| `PORT` | `3000` | Web 监听端口 |

来源必须只包含协议、主机和可选端口，不带用户信息、路径、查询参数或片段。`PUBLIC_ORIGIN` 与 Core 的公开来源采用相同安全限制：HTTPS，或主机为 `localhost`、IPv4/IPv6 loopback 的 HTTP；普通域名和内网地址不能使用公开 HTTP 来源。`CORE_ORIGIN` 可使用 `http://core:8787` 等内网 HTTP 地址。HTTPS 可由外层反向代理终止，转发到 Web；浏览器始终通过同一公开来源读取页面和 API。

PowerShell 本地运行示例：

```powershell
$env:CORE_ORIGIN = 'http://127.0.0.1:8787'
$env:PUBLIC_ORIGIN = 'http://127.0.0.1:15300'
$env:PORT = '15300'
npm --prefix web run build
npm --prefix web start
```

Core 需要开启客户端 API，并将 `MOERISE_PUBLIC_ORIGIN` 配置为同一 `http://127.0.0.1:15300`。同时配置 `MOERISE_WEB_ENABLED=true`、`MOERISE_STATE_ENCRYPTION_KEY` 及 Memos 绑定信息，具体字段见[运行与恢复](OPERATIONS.md)。HTTP 本地访问使用 loopback 来源；HTTPS 部署示例为 `PUBLIC_ORIGIN=https://notes.example.com` 和 `MOERISE_PUBLIC_ORIGIN=https://notes.example.com`。

## Docker

在仓库根目录构建：

```sh
docker build -t moerise-web ./web
docker run --rm --name moerise-web --network moerise \
  -p 3000:3000 \
  -e CORE_ORIGIN=http://core:8787 \
  -e PUBLIC_ORIGIN=https://notes.example.com \
  moerise-web
```

示例要求已创建 `moerise` 网络，Core 在其中以 `core` 为主机名提供 8787 端口，并由 HTTPS 入口将 `notes.example.com` 转发至 Web。Web 容器使用普通用户运行，不需要挂载数据库或配置 Memos PAT。镜像通过 `/health/live` 检查自身存活，`fetch` 使用 `AbortSignal.timeout(2000)` 限制请求时长；Docker 检查超时为 3 秒。

## 健康检查

| 路径 | 方法 | 成功条件 | 成功响应 |
| --- | --- | --- | --- |
| `/health/live` | GET、HEAD | 构建入口 `dist/index.html` 可读 | 200，`{"status":"ok"}` |
| `/health/ready` | GET、HEAD | 构建入口可读，且 Core 就绪检查返回 200 | 200，`{"status":"ready"}` |

构建入口读取失败时，两种检查均返回 503。就绪检查固定使用服务端 GET 请求 `CORE_ORIGIN` 的 `/health/ready`，不带浏览器 Cookie、Authorization、Origin 或其他客户端请求头，不跟随重定向，不接受目标地址参数。Core 的非 200 响应、断连和超时均映射为 503；探测总时限为 2 秒，包含连接和响应读取，持续返回数据也不会延长时限。构建入口读取失败时不请求 Core。

失败响应固定为 `{"status":"degraded"}`，不包含构建内容、私人笔记、上游响应、凭据或连接细节。HEAD 使用相同检查与状态码，响应无正文。其他方法返回 405，`Allow: GET, HEAD`，不读取构建或探测 Core。健康响应使用 `Cache-Control: no-store`；存活检查不依赖 Core 就绪状态。

## 同源请求与会话

浏览器仅请求相对路径 `/api/app/v1`。Web 向 `CORE_ORIGIN` 转发该固定前缀及其子路径，不提供任意 URL 或其他 Core 路径的代理。客户端 API 请求只要带有 `Authorization` 头即返回 403，包括空值和同时带有效 Cookie 的请求。代理保留 Cookie、Origin、CSRF 和幂等头，忽略代理来源头和目标地址头，不跟随上游重定向。

登录和写操作需要匹配 `PUBLIC_ORIGIN`；Core 继续执行会话、作者、Origin 与 CSRF 校验。Cookie 由 Core 在登录时设置，退出时清除，具有 HttpOnly 属性，前端不读取 Cookie。登录有效期固定，刷新上游凭据不会延长该期限；读取和业务响应不重写 Cookie。CSRF 和会话身份仅保存在运行内存，不写入浏览器存储。私人 API 与静态入口均返回 `Cache-Control: no-store`。

其他标签页重新登录导致 CSRF 失配时，Web 重新读取会话身份，再使用新的 CSRF 重试一次原请求。创建重试保留原正文和幂等键；退出使用同一身份恢复流程。

时间线请求使用 `state=NORMAL&order=created_desc&page_size=20`。客户端按笔记 `name` 合并分页，滚动接近末尾时通过 `next_page_token` 读取下一页，只插入新卡片或更新变化的卡片。点击时间线导航或刷新浏览器会重新读取来源，替换第一页和分页位置。可重试的分页故障显示错误并沿原游标重试，间隔从 1 秒递增至最多 30 秒；成功后恢复滚动加载，退出或手动刷新会取消旧重试。密码登录关闭与登录限流有单独说明。

退出和会话失效立即清除页面里的私人正文、账户信息和列表，取消旧请求并使旧响应失效。会话恢复使用 `GET /api/app/v1/session`，不通过浏览器凭据替代 Core 身份检查。

## 创建与结果确认

每次独立保存生成一个幂等键，提交 `{content}` 原正文。网络中断、超时、会话失效或不完整响应都会保留原正文与原键，编辑器保持原提交正文，再次点击“保存”继续同一请求。重新登录不会自动提交正文。

参数校验失败（`INVALID_ARGUMENT` 或 `UNSUPPORTED_MEDIA_TYPE`）后保留正文，清除待确认提交并恢复编辑。其他保存失败，包括来源笔记冲突、删除或上游拒绝，均保留原正文、原幂等键与同一提交，编辑器保持锁定，再次保存继续原请求。确认保存前不生成新的提交标识。

正文和待确认提交只暂存在页面内存中，退出时从页面移除，登录后重新显示；关闭或刷新页面会丢失这些内容。确认保存后清除对应提交状态，再刷新来源列表。正文通过 `textContent` 渲染。

## 产品设置

侧栏底部的设置入口支持时区、回顾数量与时长、归档开关、推送开关和多个时段。LLM 与 Embedding 分别配置，默认洞察 Prompt 可以修改并恢复默认；模型协议沿用已支持的 Provider。

凭据只显示配置状态，通过保持、替换、清空三种操作管理，保存后或离开页面即移除输入中的新密钥。飞书应用和 Webhook 展示文字、图文与记录能力，仅配置 URL 的 Webhook 可作为文字渠道。

保存使用读取到的版本。遇到版本冲突时重新加载后编辑，繁忙时可以重试；设置不写入浏览器存储。回顾日程变更从后续日期生效，推送开关立即生效。存储、资源替换与旧队列兼容规则见[产品设置](SETTINGS.md)。

## 开发检查

```sh
npm --prefix web run build
npm --prefix web test
```

专项检查覆盖固定代理路径、Cookie 和写入头传递、来源限制、来源断连，以及原正文与幂等键的重试配对。完整服务联调沿用[开发指南](DEVELOPMENT.md)中的 Memos 环境。
