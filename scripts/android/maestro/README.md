# Android Maestro 流程

此目录维护真机与 CI 共用的 UI 自动化定义、入口清单和主机校验。运行器负责隔离账号、Core 客户端 API 准备与后验检查、设备互斥、文件准备和结果目录。YAML 负责界面操作和可见结果检查。

## 入口与分组

`catalog.json` 是入口、超时、笔记准备要求和分组的唯一清单。运行器逐个调用清单中的文件，不将目录作为测试集合。默认分组严格为 `editing`、`search`、`actions`、`insights`、`navigation`、`attachments`、`language`、`home`；`home` 对应 `home-smoke`。

| 入口 | 界面覆盖 | API 后验或结果约定 |
| --- | --- | --- |
| `login` | 保留已登录会话；登录页出现时填写隔离账号并进入主页 | 已登录时检查主页，不退出账号以强制重走登录 |
| `editing` | 保留指定草稿、保存目标笔记修改、输入对照笔记修改后取消，检查对照原文与顶部草稿恢复，再新建保存 | `NOTE_NAME` 内容等于 `EDITED_TEXT`；peer 仍为 `PEER_NOTE_TEXT`；`NEW_NOTE_TEXT` 恰好一篇 |
| `search` | 以 IME 动作提交关键词、切换到 hybrid 并再次提交；点击正文和时间戳后保持主页，检查详情路由缺席；清空搜索并检查两条笔记恢复 | `QUERY_TEXT` 与 `NOTE_TEXT` 完全相同 |
| `actions` | 保留顶部草稿穿过归档、恢复和两次删除；先检查 peer 未受影响再删除 peer，检查草稿恢复后仅清该草稿 | `NOTE_NAME` 和 `PEER_NOTE_NAME` 均已删除 |
| `insights` | 新建会话、选择材料、阅读来源、查看历史、生成洞察并保存回答 | 只使用隔离模型，检查笔记正文与回答一致 |
| `insights-disconnect` | 选入独立素材，核对断流标记和普通追问的完整输入，同一会话连发两轮 | 首轮 `failed / LLM_PROTOCOL` 且保留部分正文，第二轮 `completed` 且有正文 |
| `navigation-review`、`navigation-walk`、`navigation-map` | 各自从主页检查对应场景并返回 | 回顾有结果或明确空态；漫步检查禁用提示或开始与停止；地图检查开关和全屏往返 |
| `navigation-settings` | 切换四页签，保存 QQ 渠道并检查凭据控件，返回主页验证独立草稿 | 读回 QQ 目标与 AppID、密钥保留状态；随后恢复测试渠道配置 |
| `language` | 中文与英语切换，恢复原语言设置，包括跟随系统 | 只修改隔离账号使用的语言偏好 |
| `attachments` | 打开／取消选择器，再选择唯一 txt 并保存新笔记 | `ATTACHMENT_NOTE_TEXT` 恰好一篇；附件名与字节均匹配运行器准备的文件 |

兼容短流程保持独立入口：`home-smoke`、`archive`、`insights-material`、`attachment-cancel`。`navigation` 组按回顾、漫步、地图、设置的顺序运行，`navigation.yaml` 保留完整导航入口。`archive` 完成后恢复目标笔记；`insights-material` 先通过 UI 新建独立洞察。准备笔记以 `seed_notes` 标记为准。

## 变量与隔离数据

凭据只通过 Maestro 子进程继承的环境变量提供：

| 环境变量 | 用途 |
| --- | --- |
| `MAESTRO_FIXTURE_ORIGIN` | 隔离服务地址 |
| `MAESTRO_FIXTURE_USERNAME` | 测试用户名 |
| `MAESTRO_FIXTURE_PASSWORD` | 测试密码 |

凭据只在 `subflows/ensure-login.yaml` 的登录分支输入，不写入 YAML、catalog 或 CLI 明文参数。凭据环境值应为 1—100 字符的可打印 ASCII。替换非空输入时，流程长按指定输入框、点击系统全选动作，再用一次退格删除完整内容，避免光标处于中间时留下旧文本。

其余固定变量由运行器提供，可通过 Maestro `-e` 传递：

| 变量 | 约束 |
| --- | --- |
| `APP_ID` | 已安装应用的 Android 包名 |
| `SCREENSHOT_PREFIX` | 每次执行独立的相对前缀；目录段只含 ASCII 字母、数字、下划线、连字符 |
| `NOTE_NAME`、`PEER_NOTE_NAME` | 两条不同的完整资源名，格式为 `memos/` 加字母、数字、下划线或连字符 |
| `NOTE_TEXT`、`PEER_NOTE_TEXT` | Core 官方客户端 API 创建的两条专用笔记原文 |
| `EDITED_TEXT`、`PEER_EDITED_TEXT` | 目标笔记保存的修改、对照笔记输入后取消的修改；分别只允许输入对应的独立笔记编辑器 |
| `DRAFT_TEXT`、`QUERY_TEXT` | 未完成草稿、搜索词；搜索词必须等于 `NOTE_TEXT` |
| `NEW_NOTE_TEXT` | 编辑场景中 UI 新建并保存的笔记内容 |
| `ATTACHMENT_NOTE_TEXT` | 附件场景中 UI 新建并保存的笔记内容 |
| `ATTACHMENT_NAME` | 唯一 ASCII 文件名，仅含字母、数字、下划线、连字符，并以 `.txt` 结尾 |
| `ATTACHMENT_SHA256` | 主机后验读取的附件摘要，保存在 `test-data.json` |
| `EXPECTED_ACCOUNT` | 服务端会话的显示名，缺省使用用户名 |
| `DISCONNECT_MARKER`、`FOLLOWUP_TEXT` | 已声明的断流触发标记与唯一普通追问 |
| `CHAT_TITLE` | 保留运行器的数据字段；界面没有会话命名控件，流程不写入该值 |

测试文本使用独立唯一 ASCII 标记，只含字母、数字、下划线、连字符及词间单个空格，长度最多 90。除 `QUERY_TEXT = NOTE_TEXT` 外，各种文本应不同。选择器通过 `\Q…\E` 将动态文本作为字面量匹配。

输入为空时，应用导出以 `_empty` 结尾的资源标记，例如 `note_editor_empty`。登录、搜索和编辑流程用该标记判断是否需要替换文本、草稿是否清空；空字符串选择器会被 Maestro 忽略，不能用于空输入断言。登录字段通过 Next／Done 输入动作切换和提交，每次输入前检查实际焦点；远端保存结果由运行器通过 API 独立核对。

滚动定位使用 `centerElement: true`，使目标离开列表裁剪边缘后再点击。列表笔记等待最多 65 秒，以覆盖同步完成和本机列表刷新。搜索使用输入法动作提交，文件选择器返回后检查列表内容；附件保存后单独滚动到完整附件名，避免只看到笔记正文就判断附件缺失。

删除操作展开卡片内的确认按钮后，单独滚动到完整按钮再点击，避免按钮下半部分被系统导航栏覆盖。

打开抽屉时由应用清除焦点并收键盘。搜索提交后直接等待结果。保存、取消或添加附件的控件被遮挡时，流程可以调用 `hide-keyboard.yaml`。校验器要求 `hideKeyboard` 由 `ime_visible` 明确保护，后续以目标控件可用为准。

`seed_notes: true` 的入口由运行器为每次场景独立创建两条正常笔记，并等待 Core 立即可读。主页执行同步后检查具体控件和笔记。不要在编辑、删除等场景之间复用同一组笔记。

入口应处于普通页面，导航抽屉和弹窗关闭。编辑、动作、洞察、导航写草稿前要求输入器为空或已等于同一次场景的 `DRAFT_TEXT`；其他草稿会使流程明确失败。动作场景在归档恢复后、首次删除后和两次删除结束后检查同一草稿。结束时只清除逐字匹配的测试草稿。附件保存要求输入器为空。取消选择器短流程不修改已有草稿。

新建洞察通过 `chat_session_menu` → `chat_new` 重置独立的会话状态，再完成选材。`insights` 在隔离模型生成结束后，保存默认选中的回答。运行器从 Core 读取会话和已存笔记，比对完整正文。`insights-material` 只检查选材和阅读。

地图首次进入全屏时，若 Android 的全屏介绍按钮 `com.android.systemui:id/ok` 出现，流程确认介绍后继续检查全屏退出控件。

## 附件提供者

运行器将唯一 txt 准备在设备 `Download` 目录并完成媒体扫描。`subflows/pick-download.yaml` 根据实际出现的提供者控件分派，分支分别处理：

- `pick-documentsui.yaml` 支持 AOSP／Google DocumentsUI，使用明确的资源 ID、Downloads／下载根目录及完整文件名。若根目录抽屉已经打开则直接检查其内容；选中文件后，只有 `action_menu_select` 可见时才执行确认。
- `pick-xiaomi.yaml` 支持小米 `com.android.fileexplorer`，在 `android:id/list` 内按 `file_name` 和完整文件名定位。选择前要求 `btnConfirm` 禁用，点击唯一测试文件后等待该按钮启用，再明确确认。媒体扫描后的文件应能出现在选择器的文件列表内；流程不猜测浏览目录、搜索框或坐标。

每个提供者子流程都必须独立检查 App 输入器可见、确切附件名出现及对应选择器关闭。校验器会拒绝缺少任意一项返回检查的提供者定义，避免依赖外层流程补充结果判断。

其他厂商文件管理器、未识别的确认页或权限弹窗会使检查失败并截图，不采用坐标、`optional` 或盲目再次点击。扩展提供者前，应先读取该提供者的实时界面层级并增加对应检查。

## 主机校验

使用含 `requirements.txt` 所列 PyYAML 版本的 Python 环境，从仓库根目录执行：

```powershell
python -B scripts/android/maestro/validate.py
python -B scripts/android/maestro/validate_test.py
python -B scripts/android/maestro/validate.py --check-syntax --maestro C:\maestro\bin\maestro.bat
```

前两项解析 catalog／YAML，检查源码 ID、子流程引用、参数策略及失败路径；第三项对每个 YAML 调用真实 `maestro check-syntax`，只执行离线解析。不会调用 `maestro test`、ADB、Gradle或安装操作。

运行器可以导入 `validate.py` 调用 `validate_runtime(flow, values, environment)`；也可将不含凭据的数据映射写入独立结果目录的 JSON，再执行：

```powershell
python -B scripts/android/maestro/validate.py --flow editing --parameters-json $parametersPath
```

`flow` 接受 catalog 入口名或对应 YAML 文件名。凭据仍从进程环境读取，不能混入 JSON。默认无参数检查兼容运行器已有的主机检查入口。校验错误只输出规则说明，不回显凭据或原始解析片段。

允许的命令包括已知控件的点击、断言、有限等待与滚动、ASCII `inputText`、有界 `eraseText` 和有明确元素条件的 `runFlow`。普通界面等待为 15 秒；列表刷新、归档、删除、选材保存和附件上传的结果等待为 65 秒，与客户端请求预算一致，结果出现后立即继续。`longPressOn` 只接受指定测试输入框，全选动作必须跟随已知输入框的聚焦，一次退格必须跟随全选动作。系统全选动作隐藏时，先打开已识别的文本工具栏溢出菜单。`pressKey` 的 `Enter` 只用于聚焦的登录字段或 `search_query`，分别对应 Next／Done 和 `ImeAction.Search`。时间戳匹配限定在指定笔记内，原测试的 `detail_back`、`note_detail`、`open_note_${NOTE_NAME}` 仅允许用于缺席断言。条件分支首先断言其前置状态。拒绝清数据、停止应用、网络修改、安装卸载、任意脚本、坐标、自动 retry／repeat 和吞错参数。

## 执行结果

所有启动都使用 `clearState: false`、`stopApp: false`。运行器使用 `--no-reinstall-driver`，按 catalog 超时执行并保留非零退出码。每个文件的完成钩子只截图，不在失败后执行恢复、删除或再次尝试。

截图使用 `${SCREENSHOT_PREFIX}`，报告、日志和截图保存到 Git 忽略的独立结果目录，只包含隔离账号数据。设备失联或进程终止时完成截图可能无法产生，因此还需保留 Maestro 已有调试产物。归档中途失败的笔记可能仍在归档中，由运行器根据实际状态进行独立清理。

主机解析和静态策略检查证明定义符合语法与接口约定；真机交互、输入法、厂商选择器及网络时序由真机执行记录证明。中文组合输入、生命周期及其他不适合迁移的专项继续使用 instrumentation。

## Windows 正式包真机入口

连接并授权一台 Android 真机，在仓库根目录以 PowerShell 运行：

```powershell
.\scripts\android\verify-phone.ps1
```

Python、ADB 和 Maestro 应可从同一终端运行。安装 Python 依赖使用 `python -m pip install -r scripts/android/maestro/requirements.txt`。默认读取 Git 忽略的 `.local/phone/profile.json`，`--profile` 可指定另一个档案：

```json
{
  "apk": "moerise-android.apk",
  "fixture": "connection.json",
  "sessions_root": "sessions",
  "record": false
}
```

档案中的相对路径以档案目录解析，命令行路径从当前目录解析，命令行参数优先。会话默认保存到 `.local/phone/sessions`。连接档案包含 `origin`、`username`、`password` 和 `"environment": "test"`，正式包使用 HTTPS 测试源。`test_origin` 省略时取档案的 `origin`，指定时必须匹配。多台在线真机时用 `--serial` 选定设备。

首次使用 USB ADB 进入实跑时，已有未核验账号须在设备断网退出，停在空白登录页后恢复测试网络。在交互终端确认，或以 `--confirm-ready` 声明已核对。已装包摘要不符时，机主授权覆盖安装后在设备上及时确认。覆盖安装先采集旧态。首次启动前独立采集遥测 CSV，即使 APK 已手动装好也一样。入口确认目标 Activity 启动后记录首启时间；启动尝试失败后仍可补录首启前 CSV。已启动的设备用 `--already-launched` 记录错过窗口。`--non-interactive` 在需要人工操作时保存报告并退出，可用操作卡回填再续跑。启动前证据需要提供保留原始文件时间的遥测 CSV；入口未观察到首启时，补录通过还要用 `--first-launch-at` 提供实际首启的带时区时间，CSV 须早于该时间。

来源探针先通过飞行模式和 Wi-Fi 关闭隔离设备，确认无活跃网络后启动 App。首次欢迎页保持遥测默认关闭，继续到空白登录页。入口在恢复设备网络前停止 App。通过离线检查后，仅登录档案指定的 HTTPS 测试源，再核对侧栏账号、切回主页重置搜索和归档状态、同步并读取唯一测试笔记。保留已验证的测试登录态即可非交互续跑。入口匹配 HTTPS 来源、Core 实例、用户身份，并在设备离线时核对缓存中的测试笔记标记，通过后才联网启动。全为复用结果时跳过手机来源探针。

每项依次准备独立数据、运行界面、恢复编辑态、执行服务端后验并落盘。已登记的测试行内编辑或完全匹配的草稿最多恢复一次。未知草稿保留，需要干净编辑态的后续项标记跳过。测试渠道恢复检查点以规范化来源索引，下次执行任意模块前先恢复。编辑冲突探针读回 `409 SOURCE_CHANGED` 与远端正文。

重复执行固定入口会续跑同一设备、APK、连接档案与测试源对应的活动会话。成功的 UI、服务端后验与流指纹都有效时复用该项；`--check` 可重复指定检查 ID，`--retry` 强制重跑所选项。`--baseline` 或档案的 `baseline` 字段可导入已有证据。基线 JSON 包含 `apk_sha256`、`serial`、`test_origin` 和以检查 ID 为键的 `checks` 对象。每项填写 `ui_status`、`server_verification`、`manual_status`、`evidence_paths`，自动项还需匹配 `flow_sha256`。路径相对于基线文件。服务端后验 `pending` 的自动项只有在提供原始 `test_data_path` 且能补做完整只读后验时才可复用。`legacy_handoff: true` 使用固定的 0.3.0 等价证据映射。断流场景还需给档案设置 `fail_marker` 或传 `--fail-marker`，并确认独立模型代理能按该字面标记中断响应。

`--stop-after <检查ID或场景>` 在该项落盘后停下，含已复用项。运行中可从另一个终端执行 `python -B scripts/android/phone-session.py stop --session <会话目录>`，请求在当前项完成后停下。退出码 `0` 表示适用的自动项已完成，`2` 表示到边界且此前执行无失败，`1` 表示失败、阻塞或资源恢复出错。Ctrl+C 立即中止并终止当前 Maestro 子进程树。

`report.json` 与 `report.md` 列出整份清单的覆盖、UI、服务端及人工结果、证据与观测到的最后一步。`manual-cards.md` 按待确认项列出通过条件和可复制的 `record` 命令；可加 `--evidence <文件>`、`--note <说明>`。`--record` 为每个实跑场景保留最长 180 秒录像。服务请求使用 `Moerise-Android-Acceptance/1.0` 标识；准备失败保留脱敏的步骤、方法、路径和错误码，Cloudflare 1010 记为 `CLOUDFLARE_1010`。

设备和会话写操作共享主仓库 `.local/android/device-build.lock`。`status` 与 `stop` 可在运行时访问。退出时归还本次占用的设备状态、驱动与端口。主仓库保存按设备索引的未归还资源和输入法检查点，切换设备、APK、测试源或 worktree 后仍能找回原责任。执行 `python -B scripts/android/phone-session.py cleanup --release --session <会话目录>` 结束活动会话，保留已装 App、账号数据和历史报告。再次使用固定入口会建立新会话。已有调用方仍可使用 `prepare`、`accept`、`run`、`ui`、`manual`、`record` 和 `cleanup` 子命令。正式包的自动启动从 `verify` 进入。

### 登记新检查项

新增自动项时，写 YAML 并在 `catalog.json` 登记场景，在 `maestro_runner.py` 的 `FLOW_CONTRACT_VERSIONS` 为该场景登记准备与后验契约版本，再在 `../phone-checks.json` 追加检查 ID、模块、关联交接项和场景引用。有场景专属准备或后验变化时递增其契约版本。新增目视或现场操作项直接追加 `visual` 或 `manual`，填写执行阶段、操作步骤、通过条件和证据要求。固定入口会读取追加的条目并将其纳入覆盖报告。
