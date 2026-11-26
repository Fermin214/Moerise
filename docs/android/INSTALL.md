# Android 安装与升级

Moerise Android 客户端支持 Android 10（API 29）及以上系统，发行 APK 包含 `arm64-v8a` 与 `x86_64` 架构。服务地址填写 Moerise 对外 Web 的 HTTPS 入口（仅包含协议、主机名和可选端口）。

## 安装

从对应版本的 [GitHub Releases](https://github.com/Fermin214/Moerise/releases) 下载 `moerise-android-v0.4.0.apk`，通过文件管理器安装（如系统提示，请允许文件管理器安装未知应用）。可在 Release 的 APK 详情查看 SHA-256 摘要。

也可通过 ADB 安装：

```bash
adb install moerise-android-v0.4.0.apk
```

首次启动时，欢迎页的遥测开关默认关闭。填写服务地址与绑定的 Memos 用户名、密码，登录后即可在主页写作：

- 点击保存后，笔记写入本机队列并立即显示在时间线中，网络恢复后自动上传；未点击保存的草稿仅保留在本机，不会自动提交。
- 完成本地索引同步后，时间线、标签、日历与关键词筛选均直接读取本机数据；顶部同步图标仅在同步中、待同步、离线、需重新登录或存在冲突时显示，手动同步位于侧栏底部账户区或列表下拉刷新。
- 笔记阅读、编辑、归档与删除均在主页完成；同一时间只编辑一条笔记，切换编辑目标前会自动将当前草稿落盘。

## 升级

已有正式发行版时，使用同包名（`io.github.fermin214.moerise`）、同签名且更高 `versionCode` 的发行 APK 直接覆盖安装，即可保留本机数据：

```bash
adb install -r moerise-android-v0.4.0.apk
```

从 `v0.3.0` 覆盖升级至 `v0.4.0` 会保留本机 Room 数据、已登录账号、未完成草稿、待发队列和语言设置。若签名证书不一致，Android 系统会拒绝覆盖安装。

开发包（`io.github.fermin214.moerise.dev`，应用名 Moerise Dev）与正式版数据相互独立，可并存安装；切换回正式版前请先将开发包中需要保留的草稿提交到服务端。

## 本机数据与清理

- 凭据、本地数据库和暂存附件均排除在系统云备份与设备迁移之外。
- 卸载应用或在系统设置中清除应用数据会删除本机草稿；在应用侧栏账户菜单选择「删除本机账号数据」仅清除当前账号的本机数据并退出登录，不影响已同步到 Memos 的笔记。

## 发行附件

公开 Release 中的 Android 安装包为签名 APK。`Main CI` 的 `android-release` 产物保留 `.apk.sha256`、`.apk.json`、`-notices.zip`、`INSTALL.md` 和 `mapping.txt`，供发布前核验。开发构建与本地签名流程见 [Android 开发指南](https://github.com/Fermin214/Moerise/blob/main/docs/ANDROID.md)。
