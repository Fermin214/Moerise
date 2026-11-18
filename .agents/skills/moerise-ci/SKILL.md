---
name: moerise-ci
description: 修改 Moerise CI 任务名、排查 PR 合并阻塞或 GitHub Actions 失败时使用。核对分支规则，按提交和运行次数定位首个异常步骤及诊断产物。
---

从仓库根目录运行，使用已登录的 `gh` 和 `jq`。

- 拆分、矩阵化或重命名任务时，推送前运行 `bash .agents/skills/moerise-ci/inspect.sh workflow main`（需要 Python 3 和 PyYAML）。它读取目标分支生效的 ruleset，检查每个绑定 GitHub Actions 的必需名称在 `checks.yml` 中是否恰好有一个固定名称的普通任务。矩阵任务可并行执行，由固定名称的任务汇总结果。任一适用任务失败时，汇总也须失败。
- 查询 PR 用 `bash .agents/skills/moerise-ci/inspect.sh pr <编号>`。对照目标分支的必需名称、提交上实际出现的检查和 GitHub 的合并状态。尚在排队的任务可能稍后才出现。查看历史提交时，远端规则反映查询时的配置。
- 查询一次运行用 `bash .agents/skills/moerise-ci/inspect.sh run <ID>`。每个 attempt 有独立日志，早先的 attempt 只列异常任务。重跑前按失败任务的时间和产物 ID 下载证据到 `.local/ci/`；同名产物可能来自不同 attempt。超时或取消时打开该任务的完整日志，`--log-failed` 可能没有输出。

- Android 设备失败时，下载对应 `android-device-<suite>` 或 `android-release-checks` 到 `.local/`。从 `result.json`、JUnit 或 `device-results.tsv` 找到首个失败动作，再对照 UI 树、截图和 Core/Memos 后验。目标笔记在列表中不可见时，先查同批数据的筛选条件、排序和分页。
- 最低 API、测试 CA 或 ADB 流程有改动时，可对已推送的分支单跑 `gh workflow run android-release.yml --ref <分支>`。它使用临时签名和 APK 声明的最低 API，在 `google_apis` x86_64 镜像上执行 `scripts/android/release-smoke.sh`。检查 ADB 等待有上限、应用启动前 SELinux 已恢复 `Enforcing`，再用同一个 APK 完成 HTTPS 登录、写入读回和强停恢复。
- 生命周期用例超时时，先从设备产物的 `*-threads.txt` 和 JUnit 找到首个异常及对应的测试操作，再查看清理阶段的异常。复测同一提交用 `gh run rerun <ID> --failed`。复测通过时保留首次失败的结论，只有定位原因并验证修复后才关闭缺陷。
- Linux 镜像失败时，从失败架构的步骤日志查找容器退出码与首个 HTTP 探测错误。用同一提交的 `scripts/release/build-images.sh` 复现，容器进程启动和健康端点返回成功是两个检查点。
- 用日志区分产品错误、测试数据或断言错误、运行环境中断。证据缺失时指出缺哪份产物。评估提速时比较相同检查范围和缓存条件，分别记录单次关键路径，以及从首次触发到复测通过的总等待时间。
- 汇报 PR 可合并前，重新读取 `headRefOid`，确认对应提交的 `PR gate` 及适用任务都已成功。Main 发布依据是同一提交的成功 Main CI 和匹配的产物。
