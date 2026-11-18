---
name: moerise-ci
description: 根据 Moerise PR 编号或 GitHub Actions 运行 ID 定位 CI 失败，核对提交、失败步骤、同类成功记录和诊断产物。
---

从仓库根目录运行 `bash .agents/skills/moerise-ci/inspect.sh pr <编号>` 或 `bash .agents/skills/moerise-ci/inspect.sh run <ID>`。PR 模式先读最新提交，再找该提交的 Checks。运行模式也可用于已结束的 Main CI。脚本列出每个任务的首个失败步骤、日志入口、产物和先前同名工作流的成功记录。比较两次运行时核对任务名和检查范围。

- Android 设备失败时，下载对应 `android-device-<suite>` 或 `android-release-checks` 到 `.local/`。从 `result.json`、JUnit 或 `device-results.tsv` 找到首个失败动作，再对照 UI 树、截图和 Core/Memos 后验。目标笔记在列表中不可见时，先查同批数据的筛选条件、排序和分页。
- Linux 镜像失败时，从失败架构的步骤日志查找容器退出码与首个 HTTP 探测错误。用同一提交的 `scripts/release/build-images.sh` 复现，容器进程启动和健康端点返回成功是两个检查点。
- 用日志区分产品错误、测试数据或断言错误、运行环境中断。遇到证据缺失时指出缺哪份产物。重跑产生的多次记录按同一失败步骤与原因归并。
- 汇报 PR 可合并前，重新读取 `headRefOid`，确认对应提交的 `PR gate` 及适用任务都已成功。Main 发布依据是同一提交的成功 Main CI 和匹配的产物。
