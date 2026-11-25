---
name: moerise-ci
description: 核对 Moerise 验收分支与 PR 的范围、修改 CI 任务名、排查合并阻塞或 GitHub Actions 失败时使用。
---

从仓库根目录运行。`scope` 只读本地 Git 对象，查询 GitHub 使用已登录的 `gh` 和 `jq`。

- 并行功能共用开发目录时，从目标分支建独立工作树。推送验收分支前先 `git fetch origin main`，再运行 `bash .agents/skills/moerise-ci/inspect.sh scope origin/main <功能的第一个提交> [候选提交]`。首提交取自功能开发记录。命令列出候选 PR 的全部提交和文件，并指出混入的前置或旁支提交。对照交付内容核对文件，尤其是服务端、客户端、模板、文档和验收脚本。依赖另一项功能时，应显式选择对应的 PR 基线。
- 定向设备验收尽量使用交付分支；已有 PR 全量运行覆盖同一 SHA 和用例时直接看该运行的设备结果。判断两个 PR 能否一起合入，使用 `git merge-tree --write-tree --name-only --no-messages <提交一> <提交二>`，记录对应提交和实际冲突文件。

- 拆分、矩阵化或重命名任务时，推送前运行 `bash .agents/skills/moerise-ci/inspect.sh workflow main`（需要 Python 3 和 PyYAML）。它读取目标分支生效的 ruleset，检查每个绑定 GitHub Actions 的必需名称在 `checks.yml` 中是否恰好有一个固定名称的普通任务。矩阵任务可并行执行，由固定名称的任务汇总结果。任一适用任务失败时，汇总也须失败。
- 调整分组或矩阵成员后也运行 `bash .agents/skills/moerise-ci/inspect.sh workflow main`。CI 交接记录 `head SHA`、`run ID`、`attempt` 和 `PR gate` 状态。
- 查询 PR 用 `bash .agents/skills/moerise-ci/inspect.sh pr <编号>`。对照目标分支的必需名称、提交上实际出现的检查和 GitHub 的合并状态。尚在排队的任务可能稍后才出现。查看历史提交时，远端规则反映查询时的配置。
- 查询一次运行用 `bash .agents/skills/moerise-ci/inspect.sh run <ID>`。每个 attempt 有独立日志，早先的 attempt 只列异常任务。重跑前按失败任务的时间和产物 ID 下载证据到 `.local/ci/`；同名产物可能来自不同 attempt。超时或取消时打开该任务的完整日志，`--log-failed` 可能没有输出。

- 预取、刷新或重连可能与用户操作并发。Web 和设备流程都通过用户入口触发操作，等待目标正文、月份、卡片位置或已保存的结果。模拟请求失败时按方法和目标资源注入一次故障。页签按名称或稳定 ID 选择。测试滚动位置时，记录可见卡片的键和偏移，操作后直接比较，别让测试自己滚回原位。
- 草稿恢复用最少的笔记。跨页编辑用隔离服务预置超过一页的数据，再从界面滚动触发分页。等待超时时记录卡在哪一步、已加载多少条、还有没有下一页、同步是否在跑，再判断要改测试还是产品。
- Android 设备失败时，下载对应 `android-device-<suite>` 或 `android-release-checks` 到 `.local/`。在同一次执行内按 `result.json` 的 `last_step` 找到 `commands.json`，按动作顺序查首错，再看 JUnit、UI 树和服务端读回结果。结束时截图也可能失败并出现在 JUnit 摘要里。洞察生成失败时，在 `device-fixture-fixture.log` 中查 `chat_message` 的 `api_request`、`api_response` 或 `api_interrupted`，与 UI 及 Core 中保存的回答对照。HTTP 完成后仍需核实回答的业务状态。目标笔记在列表中不可见时，先查同批数据的筛选条件、排序和分页。
- 最低 API、测试 CA 或 ADB 流程有改动时，可对已推送的分支单跑 `gh workflow run android-release.yml --ref <分支>`。它使用临时签名和 APK 声明的最低 API，在 `google_apis` x86_64 镜像上执行 `scripts/android/release-smoke.sh`。检查 ADB 等待有上限、应用启动前 SELinux 已恢复 `Enforcing`，再用同一个 APK 完成 HTTPS 登录、写入读回和强停恢复。
- 生命周期用例超时时，先从设备产物的 `*-threads.txt` 和 JUnit 找到首个异常及对应的测试操作，再查看清理阶段的异常。复测同一提交用 `gh run rerun <ID> --failed`。复测通过时保留首次失败的结论，只有定位原因并验证修复后才关闭缺陷。
- Linux 镜像失败时，从失败架构的步骤日志查找容器退出码与首个 HTTP 探测错误。用同一提交的 `scripts/release/build-images.sh` 复现，容器进程启动和健康端点返回成功是两个检查点。
- 用日志区分产品错误、测试数据或断言错误、运行环境中断。证据缺失时指出缺哪份产物。评估提速时比较相同检查范围和缓存条件，分别记录单次关键路径，以及从首次触发到复测通过的总等待时间。
- 对比耗时前固定 `scope`、套件覆盖和各项缓存的冷／热口径，再记录排队时间和墙钟时间。排查慢 CI 先沿 `needs` 与矩阵汇总找出决定 `PR gate` 完成时间的最长依赖链，再定位链上的最慢步骤。
- 回答性能验收问题时，列明测的是哪个动作、在哪种设备上测、有无实测值。涉及可选遥测时，分别说明手机是否计数、数据发给谁、默认构建能否外发，依据见 `docs/PRIVACY.md`。
- 汇报 PR 可合并前，重新读取 `headRefOid`，确认对应提交的 `PR gate` 及适用任务都已成功。Main 发布依据是同一提交的成功 Main CI 和匹配的产物。
