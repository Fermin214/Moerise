---
name: moerise-android-acceptance
description: 用于 Android 正式包真机交接、Windows 固定入口续跑、Maestro 手机来源探针与真机验收证据核对。
---

从仓库根目录运行 `.\scripts\android\verify-phone.ps1`。按 [检查清单](../../../scripts/android/phone-checks.json) 选项，按 [Windows 真机入口与操作卡](../../../scripts/android/maestro/README.md#windows-正式包真机入口) 准备档案和回填证据。

- 会话和基线绑定 `serial`、`apk_sha256`、`test_origin`。有效复用同时要求场景指纹、UI、服务端终态和可读取的证据一致。先核对 `session.json` 与同一运行的 `report.json`，再按需用 `--check` 续跑。
- 按清单分开采集 `pre_install` 覆盖安装前状态、`pre_launch` 首启前遥测 CSV 和 `post_run` 执行证据。保留 CSV 文件时间和带时区的首启时间。
- 来源未知时先确认设备离线及账号界面，校验测试源、用户身份和探针笔记后才允许同步写回。还原网络前先停 App。探针失败时保留已成立复用和能力跳过项，记录待跑项的阻塞阶段。
- 先恢复归属明确的输入法、网络、驱动、端口和测试渠道，再独立结算各项。独立后续项可在单项业务失败后执行。实际重跑的新失败取代同项历史通过，尚未执行的项保留历史有效记录。
- `--stop-after` 和 `stop` 都在该项结果保存后响应。核对停止点、后续项 `not_run`、历史证据和清理结果，再解释退出码。

宿主回归使用 `python -B scripts/android/phone-session.test.py`、`python -B scripts/android/maestro-runner.test.py` 和 `python -B scripts/android/maestro/validate.py`。
