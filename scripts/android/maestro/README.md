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
| `insights` | UI 新建独立洞察、添加指定材料、收起／展开材料、阅读来源、打开／关闭历史 | 不调用模型，不依赖旧洞察的材料 |
| `navigation` | 回顾、漫步、地图、五个设置页签、主页；检查跨页面草稿 | 回顾检查有内容或明确空结果；漫步检查禁用提示或开始／停止；地图按设置检查可用分支 |
| `language` | 中文与英语切换，恢复原语言设置，包括跟随系统 | 只修改隔离账号使用的语言偏好 |
| `attachments` | 打开／取消选择器，再选择唯一 txt 并保存新笔记 | `ATTACHMENT_NOTE_TEXT` 恰好一篇；附件名与字节均匹配运行器准备的文件 |

兼容短流程保持独立入口：`home-smoke`、`archive`、`insights-material`、`attachment-cancel`。`navigation.yaml` 同时保留原路径并覆盖完整导航。`archive` 完成后恢复目标笔记；`insights-material` 也先通过 UI 新建独立洞察。短流程的 `seed_notes` 标记以清单为准，不能从所属分组推断。

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
| `CHAT_TITLE` | 保留运行器的数据字段；界面没有会话命名控件，流程不写入该值 |

测试文本使用独立唯一 ASCII 标记，只含字母、数字、下划线、连字符及词间单个空格，长度最多 90。除 `QUERY_TEXT = NOTE_TEXT` 外，各种文本应不同。选择器通过 `\Q…\E` 将动态文本作为字面量匹配。

输入为空时，应用导出以 `_empty` 结尾的资源标记，例如 `note_editor_empty`。登录、搜索和编辑流程用该标记判断是否需要替换文本、草稿是否清空；空字符串选择器会被 Maestro 忽略，不能用于空输入断言。保存后先收起键盘并返回顶部，再检查新建输入器；远端保存结果由运行器通过 API 独立核对。

滚动定位使用 `centerElement: true`，使目标离开列表裁剪边缘后再点击。列表笔记等待最多 65 秒，以覆盖同步完成和本机列表刷新。搜索提交后和文件选择器返回后先收起键盘，再检查列表内容；附件保存后单独滚动到完整附件名，避免只看到笔记正文就判断附件缺失。

`seed_notes: true` 的入口由运行器为每次场景独立创建两条正常笔记，并等待 Core 立即可读。主页执行同步后检查具体控件和笔记。不要在编辑、删除等场景之间复用同一组笔记。

入口应处于普通页面，导航抽屉和弹窗关闭。编辑、动作、洞察、导航写草稿前要求输入器为空或已等于同一次场景的 `DRAFT_TEXT`；其他草稿会使流程明确失败。动作场景在归档恢复后、首次删除后和两次删除结束后检查同一草稿。结束时只清除逐字匹配的测试草稿。附件保存要求输入器为空。取消选择器短流程不修改已有草稿。

新建洞察通过 `chat_session_menu` → `chat_new` 重置独立的会话状态，再完成选材。UI 没有会话命名入口；`CHAT_TITLE` 不能用作界面命名成功的断言。洞察流程保留新建的测试会话供运行器检查，不删除旧会话或发送模型请求。

## 附件提供者

运行器将唯一 txt 准备在设备 `Download` 目录并完成媒体扫描。`subflows/pick-download.yaml` 根据实际出现的提供者控件分派，分支分别处理：

- `pick-documentsui.yaml` 支持 AOSP／Google DocumentsUI，使用明确的资源 ID、Downloads／下载根目录及完整文件名。若根目录抽屉已经打开则直接检查其内容；选中文件后，只有 `action_menu_select` 可见时才执行确认。
- `pick-xiaomi.yaml` 支持小米 `com.android.fileexplorer`，在 `android:id/list` 内按 `file_name` 和完整文件名定位。选择前要求 `btnConfirm` 禁用，点击唯一测试文件后等待该按钮启用，再明确确认。媒体扫描后的文件应能出现在选择器的文件列表内；流程不猜测浏览目录、搜索框或坐标。

每个提供者子流程都必须独立检查 App 输入器可见、确切附件名出现及对应选择器关闭。校验器会拒绝缺少任意一项返回检查的提供者定义，避免依赖外层流程补充结果判断。

地图首次进入全屏时，若 Android 的全屏介绍按钮 `com.android.systemui:id/ok` 出现，流程确认介绍后继续检查全屏退出控件。

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

允许的命令包括已知控件的点击、断言、有限等待与滚动、ASCII `inputText`、有界 `eraseText` 和有明确元素条件的 `runFlow`。普通界面等待为 15 秒；列表刷新、归档、删除、选材保存和附件上传的结果等待为 65 秒，与客户端请求预算一致，结果出现后立即继续。`longPressOn` 只接受指定测试输入框，全选动作必须跟随已知输入框的聚焦，一次退格必须跟随全选动作。系统全选动作隐藏时，先打开已识别的文本工具栏溢出菜单。搜索提交限定在聚焦的 `search_query` 上执行 `pressKey` 的 `Enter`，对应界面的 `ImeAction.Search`；收起键盘不替代提交。时间戳匹配限定在指定笔记内，原测试的 `detail_back`、`note_detail`、`open_note_${NOTE_NAME}` 仅允许用于缺席断言。条件分支首先断言其前置状态。拒绝清数据、停止应用、网络修改、安装卸载、任意脚本、坐标、自动 retry／repeat 和吞错参数。

## 执行结果

所有启动都使用 `clearState: false`、`stopApp: false`。运行器使用 `--no-reinstall-driver`，按 catalog 超时执行并保留非零退出码。每个文件的完成钩子只截图，不在失败后执行恢复、删除或再次尝试。

截图使用 `${SCREENSHOT_PREFIX}`，报告、日志和截图保存到 Git 忽略的独立结果目录，只包含隔离账号数据。设备失联或进程终止时完成截图可能无法产生，因此还需保留 Maestro 已有调试产物。归档中途失败的笔记可能仍在归档中，由运行器根据实际状态进行独立清理。

主机解析和静态策略检查证明定义符合语法与接口约定；真机交互、输入法、厂商选择器及网络时序由真机执行记录证明。中文组合输入、生命周期及其他不适合迁移的专项继续使用 instrumentation。
