# Android Maestro 流程

这些流程使用已安装、已登录的 Moerise，通过 Compose 导出的资源 ID 操作界面。主机脚本只解析 YAML、检查源码 ID 和流程策略，不连接设备。

| 入口 | 行为 |
| --- | --- |
| `home-smoke.yaml` | 进入主页，聚焦已有输入器，检查保存控件可见并截图。 |
| `navigation.yaml` | 分别执行主页→设置→主页、主页→洞察→主页。 |
| `archive.yaml` | 将 `NOTE_NAME` 指定的已有笔记归档，从账号菜单进入归档页，再恢复到主页。 |
| `insights-material.yaml` | 直接进入洞察，按标签／逐条选材，添加指定笔记，截图后移除该材料并返回主页。 |
| `attachment-cancel.yaml` | 打开 Android 系统文件选择器，执行一次系统返回，回到输入器并截图。 |

`subflows/home.yaml` 复用主页导航；`subflows/settings-roundtrip.yaml` 和 `subflows/insights-roundtrip.yaml` 各自从主页开始，也可作为独立入口。调用具体 YAML 文件，不将整个目录作为测试集合。

## 输入与准备条件

| 变量 | 使用范围 | 约束 |
| --- | --- | --- |
| `APP_ID` | 全部流程 | runner 提供已安装应用的 Android 包名；流程统一使用 `${APP_ID}`。 |
| `SCREENSHOT_PREFIX` | 全部流程 | runner 提供本次执行专用的相对文件前缀，如 `run_20261116/archive`；只使用字母、数字、下划线、连字符及分隔目录的 `/`。 |
| `NOTE_NAME` | 归档、洞察选材 | 已有唯一测试笔记的完整资源名，格式为 `memos/` 加字母、数字、下划线或连字符；不是笔记标题。 |

使用已登录、可访问服务的隔离测试账号。入口应处于普通页面，导航抽屉、弹窗和系统选择器应关闭，键盘应收起。主页输入器、保存控件以及导航目标应在可见范围。归档场景的测试笔记应为正常状态，并位于主页与归档页可见范围；洞察场景的测试笔记应位于候选首屏，且尚未加入该洞察的材料。运行前在隔离数据集中准备专用笔记和适合选材的洞察，再将笔记资源名传给 runner。

流程不输入、清除或保存草稿。已有未完成草稿可用于观察跨页面返回后的内容；自动检查覆盖输入器和控件可达性，草稿逐字一致性及中文组合输入继续由 instrumentation 覆盖。保存按钮在空草稿下可以禁用，主页冒烟只检查其可见性。

笔记归档／恢复使用同一个 `archive_note_${NOTE_NAME}` ID；归档入口为 `open_menu` → `account_menu` → `nav_archive`。洞察直接使用 `nav_insights`，选材页签为 `chat_picker_tab_1`。地图 ID 为 `nav_map`，流程集合不涉及地图。

## 主机检查

在仓库根目录使用 Python 3.11 或更高版本。校验依赖固定在 `requirements.txt`：

```powershell
python -m pip install -r scripts/android/maestro/requirements.txt
python -B scripts/android/maestro/validate.py
python -B scripts/android/maestro/validate_test.py
```

`validate.py` 检查两段 YAML、重复键、子流程引用和循环、源码 ID、步骤标签、15 秒等待及完成截图。命令和选择器采用白名单，拒绝清数据、停止应用、安装／卸载、网络设置、脚本执行、坐标点击、文本输入、保存草稿、自动 retry／repeat 和 `optional`。

runner 可在连接设备前同时检查参数：

```powershell
& $python -B scripts/android/maestro/validate.py `
    --flow archive.yaml `
    --app-id $appId `
    --screenshot-prefix $screenshotPrefix `
    --note-name $noteName
if ($LASTEXITCODE -ne 0) { throw 'Maestro flow validation failed' }
```

主机检查不启动 Maestro，也不调用 ADB。其通过结果只表示定义、参数和静态约束符合要求；设备上的实际行为以独立设备执行记录为依据。

## runner 调用

Maestro CLI 加入 `PATH` 后，runner 分别为每个入口分配结果目录、截图前缀及报告文件。
将设备序列号传给 `$serial`，应用包名传给 `$appId`，测试笔记资源名传给 `$noteName`；
`$resultDir`、`$debugDir`、`$reportPath` 指向 `.local/android/` 下的独立运行目录。
下面是设备执行的调用接口；其中变量由 runner 设置：

```powershell
maestro test --udid $serial `
    --no-reinstall-driver `
    --test-output-dir $resultDir `
    --debug-output $debugDir `
    --format junit `
    --output $reportPath `
    -e "APP_ID=$appId" `
    -e "SCREENSHOT_PREFIX=$screenshotPrefix" `
    -e "NOTE_NAME=$noteName" `
    scripts/android/maestro/archive.yaml
if ($LASTEXITCODE -ne 0) { throw 'Maestro archive flow failed; retain its result directory' }
```

其余入口替换最后一个 YAML 路径；主页、导航及附件取消无需传 `NOTE_NAME`。路径和变量中只使用测试标识，不包含服务地址、账号或凭据。

YAML、子流程、校验脚本、依赖清单及本文档随代码维护在 Git 中。
JUnit 报告、日志、截图、设备信息和连接档案写入 Git 忽略的 `.local/android/`。
界面资源 ID 或导航行为变化时，同步修改对应流程，并执行主机检查。

`launchApp` 显式使用 `clearState: false`、`stopApp: false`。每个文件的 `onFlowComplete` 只截图，成功或失败都会执行；中途失败不导航、不恢复笔记、不再次尝试。保留非零退出码、原始报告、调试输出和截图。归档中途失败可能留下已归档的测试笔记，由 runner 依据实际笔记状态单独恢复，避免重新执行归档动作。

所有截图通过 `${SCREENSHOT_PREFIX}` 命名，截图位于 Maestro 为该执行建立的产物目录中。前缀使用相对路径，不能用 `..` 或绝对路径跨出产物目录。进程被终止或设备失去连接时，完成钩子可能无法截图，因此 runner 还应保留 Maestro 已产生的失败截图与日志。

附件取消通过输入器消失和重新出现判断页面切换，不猜测系统文件管理器的 ID、文字或确认按钮。真实文件选取需要读取设备的实时层级逐步执行。

## 语法参考

- [launchApp](https://docs.maestro.dev/reference/commands-available/launchapp)
- [runFlow](https://docs.maestro.dev/reference/commands-available/runflow)
- [extendedWaitUntil](https://docs.maestro.dev/reference/commands-available/extendedwaituntil)
- [完成钩子](https://docs.maestro.dev/maestro-flows/flow-control-and-logic/hooks)
- [takeScreenshot](https://docs.maestro.dev/reference/commands-available/takescreenshot)
- [报告与产物目录](https://docs.maestro.dev/maestro-flows/workspace-management/test-reports-and-artifacts)
