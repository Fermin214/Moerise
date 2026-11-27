package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.time.ZoneId
import java.util.TimeZone
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull

@Composable
fun SettingsScreen(
    mobile: MobileController,
    strings: Strings,
    onMenu: () -> Unit,
    onSearch: () -> Unit,
) {
    val controller = mobile.settings
    val state by controller.state.collectAsState()
    val language by mobile.session.language.collectAsState()
    val preferencesError by mobile.session.preferencesError.collectAsState()
    val sync by mobile.sync.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    var tab by rememberSaveable { mutableIntStateOf(0) }
    var editor by remember { mutableStateOf<String?>(null) }
    var discard by remember { mutableStateOf(false) }
    val scroll = rememberScrollState()
    val haptic = rememberMoeriseHaptic()
    fun edit(target: String) { controller.discardDraft(); editor = target }
    fun close() {
        if (state.busy) return
        if (controller.dirty()) discard = true else editor = null
    }

    LaunchedEffect(controller) { controller.load() }
    LaunchedEffect(controller, tab) { controller.selectTab(tab) }
    LaunchedEffect(state.status) {
        if (state.status in setOf("SETTINGS_SAVED", "SETTINGS_UNCHANGED")) editor = null
    }
    DisposableEffect(controller) { onDispose { controller.leave() } }

    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("settings_page")) {
        WebToolbar(strings, onMenu = onMenu, onSearch = onSearch)
        Box(Modifier.weight(1f).fillMaxWidth().verticalScroll(scroll).imePadding()) {
            Column(
                Modifier.align(Alignment.TopCenter).widthIn(max = 992.dp).fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 20.dp),
                verticalArrangement = Arrangement.spacedBy(14.dp),
            ) {
                Text(
                    strings.text("设置", "Settings"),
                    fontSize = 22.sp,
                    lineHeight = 30.8.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.semantics { heading() },
                )
                WebTabs(
                    listOf(
                        strings.text("通用", "General"),
                        strings.text("检索与观星", "Search & stargazing"),
                        strings.text("回顾与推送", "Review & delivery"),
                        strings.text("隐私", "Privacy"),
                    ),
                    tab, { if (!state.busy) tab = it }, tagPrefix = "settings_tab", enabled = !state.busy,
                )
                if (tab == 0) SettingsSection(strings.text("笔记同步", "Note sync")) {
                    val lastSync = sync.lastSyncAt?.let { displayServiceTime(java.time.Instant.ofEpochMilli(it).toString(),
                        connection.profile?.timezone ?: "UTC") }
                    SettingsRow(
                        strings.text("上次同步", "Last sync"),
                        hint = lastSync ?: strings.text("尚未同步", "No sync yet"),
                        separator = false,
                    ) {
                        SettingsGhost(strings.text("立即同步", "Sync now"),
                            LocalWebSyncAction.current ?: mobile::synchronize,
                            modifier = Modifier.testTag("settings_sync"))
                    }
                }
                when {
                    tab == 3 -> PrivacySettings(mobile.privacy, strings)
                    state.loading -> SettingsHint(strings.text("正在加载设置…", "Loading settings…"))
                    state.snapshot == null -> {
                        state.error?.let { SettingsMessage(settingsMessage(it, strings), error = true) }
                        SettingsGhost(
                            strings.text("重新加载设置", "Reload settings"),
                            controller::load,
                            enabled = !state.busy,
                            modifier = Modifier.testTag("settings_reload"),
                        )
                    }
                    else -> {
                        when (tab) {
                            0 -> SettingsSection(strings.text("通用", "General")) {
                                SettingsRow(strings.text("语言", "Language")) {
                                    SettingsSelect(
                                        language.name,
                                        listOf(
                                            UiLanguage.SYSTEM.name to strings.text("跟随系统", "Follow system"),
                                            UiLanguage.CHINESE.name to "简体中文",
                                            UiLanguage.ENGLISH.name to "English",
                                        ),
                                        strings.text("语言", "Language"),
                                        enabled = !state.busy,
                                        tag = "settings_language",
                                    ) { value -> mobile.session.language(UiLanguage.valueOf(value)) }
                                }
                                SettingsSummary(strings.text("时区", "Timezone"), state.values["timezone"].orEmpty(), strings) { edit("timezone") }
                                if (preferencesError != null) SettingsMessage(
                                    strings.text(
                                        "语言设置未能保存，请重新选择。",
                                        "Could not save the language setting. Select it again.",
                                    ), error = true,
                                )
                            }
                            1 -> {
                                SettingsModels(state, strings) { edit("embedding") }
                                SettingsSection(strings.text("观星", "Stargazing")) {
                                    SettingsCheckbox(controller, state, "stargazing.enabled",
                                        strings.text("启用观星", "Enable stargazing"), atomic = true)
                                    if (state.values["stargazing.enabled"] == "true" && state.values["embedding.enabled"] == "true") {
                                        SettingsRow(strings.text("星图更新", "Star map updates"),
                                            hint = strings.text("每日 03:00 更新", "Updates daily at 03:00"), separator = false) {
                                            SettingsGhost(strings.text("重新生成星图", "Rebuild star map"), controller::rebuildMap,
                                                enabled = !state.rebuildingMap && !state.conflict,
                                                modifier = Modifier.testTag("settings_rebuild_map"))
                                        }
                                    }
                                }
                            }
                            2 -> {
                                SettingsReview(controller, state, strings, ::edit)
                                SettingsChannels(state, strings, ::edit)
                                SettingsHistory(controller, state, strings)
                            }
                        }
                        if (editor == null) state.error?.let { SettingsMessage(settingsMessage(it, strings), error = true) }
                        if (editor == null && state.conflict) SettingsGhost(strings.text("重新加载设置", "Reload settings"),
                            controller::reload, enabled = !state.loading, modifier = Modifier.testTag("settings_reload"))
                    }
                }
                if (tab == 0) connection.profile?.let { profile ->
                    SettingsSection(strings.text("账号", "Account")) {
                        SettingsRow(profile.displayName.trim().ifBlank { profile.userName.removePrefix("users/") }, separator = false) {
                            SettingsGhost(strings.text("退出登录", "Sign out"), mobile::askLogout,
                                modifier = Modifier.testTag("account_logout"))
                        }
                    }
                }
            }
        }
    }
    editor?.let { target ->
        val title = when (target) {
            "timezone" -> strings.text("时区", "Timezone")
            "review" -> strings.text("每日回顾", "Daily review")
            "times" -> strings.text("推送时段", "Delivery times")
            "embedding" -> strings.text("向量模型", "Embedding model")
            else -> channelLabel(target, strings)
        }
        val primarySecret = SettingsForm.channelSecrets[target]
        val check = when {
            target == "embedding" -> state.values["embedding.enabled"] == "true" && state.secrets["embedding_api_key"]?.action != "clear"
            primarySecret != null -> state.secrets[primarySecret]?.action == "replace" ||
                state.secrets[primarySecret]?.action != "clear" &&
                state.snapshot.settingsObject("secrets").settingsObject(primarySecret).settingsBoolean("configured")
            else -> false
        }
        AlertDialog(onDismissRequest = ::close, title = { Text(title) }, text = {
            Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState())) {
                when (target) {
                    "timezone" -> SettingsTimezone(controller, state, strings)
                    "review" -> SettingsReviewFields(controller, state, strings)
                    "times" -> SettingsTimes(controller, state, strings)
                    "embedding" -> SettingsModel(controller, state, strings)
                    else -> SettingsChannelFields(target, controller, state, strings)
                }
                if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
                state.error?.let { SettingsMessage(settingsMessage(it, strings), error = true) }
                if (state.conflict) SettingsGhost(strings.text("重新加载设置", "Reload settings"),
                    controller::reload, enabled = !state.loading, modifier = Modifier.testTag("settings_reload"))
            }
        }, confirmButton = {
            WebAction(if (check) strings.text("测试并保存", "Test and save") else strings.text("保存", "Save"),
                { haptic(); controller.save(target.takeIf { it == "embedding" || it in SettingsForm.channelSecrets }) },
                enabled = !state.busy && !state.conflict && !state.loading, modifier = Modifier.testTag("settings_save"))
        }, dismissButton = { SettingsGhost(strings.text("关闭", "Close"), ::close, enabled = !state.busy) })
    }
    if (discard) AlertDialog(onDismissRequest = { discard = false },
        title = { Text(strings.text("放弃未保存的修改？", "Discard unsaved changes?")) },
        confirmButton = { TextButton({ controller.discardDraft(); discard = false; editor = null }) {
            Text(strings.text("放弃修改", "Discard changes"))
        } }, dismissButton = { TextButton({ discard = false }) { Text(strings.text("继续编辑", "Keep editing")) } })
}

@Composable
private fun SettingsSummary(label: String, detail: String, strings: Strings, edit: () -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, fontSize = 14.sp)
            if (detail.isNotBlank()) SettingsHint(detail)
        }
        SettingsGhost(strings.text("编辑", "Edit"), edit)
    }
    HorizontalDivider(color = Color(0xFFF0F4F9))
}

@Composable
private fun SettingsTimezone(controller: SettingsController, state: SettingsUiState, strings: Strings) {
    val zones = remember { ZoneId.getAvailableZoneIds().sorted() }
    val value = state.values["timezone"].orEmpty()
    SettingsField(controller, state, "timezone", strings.text("时区", "Timezone"))
    SettingsGhost(strings.text("采用设备时区", "Use device timezone"),
        { controller.change("timezone", TimeZone.getDefault().id) }, enabled = !state.busy)
    val suggestions = remember(value) { zones.asSequence().filter { value.isBlank() || it.contains(value, ignoreCase = true) }
        .filter { it != value }.take(6).toList() }
    for (zone in suggestions) SettingsGhost(zone, { controller.change("timezone", zone) }, enabled = !state.busy)
}

@Composable
private fun SettingsReview(controller: SettingsController, state: SettingsUiState, strings: Strings, edit: (String) -> Unit) {
    SettingsSection(strings.text("每日回顾", "Daily review")) {
        SettingsSummary(strings.text("回顾规则", "Review rules"), strings.text("${state.values["review.count"]} 条",
            "${state.values["review.count"]} notes"), strings) { edit("review") }
        val kind = (state.atomicValues["channel.kind"] ?: state.values["channel.kind"]).orEmpty()
        SettingsCheckbox(controller, state, "notifications.enabled", strings.text("开启回顾推送", "Enable review delivery"),
            enabled = kind != "none", atomic = true,
            hint = if (kind == "none") strings.text("请先选择定时推送渠道。", "Choose a scheduled delivery channel first.") else "")
        val haptic = rememberMoeriseHaptic()
        SettingsRow(strings.text("定时回顾发往", "Send scheduled reviews to")) {
            SettingsSelect(kind, channelChoices(strings), strings.text("定时回顾发往", "Send scheduled reviews to"),
                enabled = !state.conflict, tag = "settings_channel_kind") {
                haptic(); controller.saveAtomic("channel.kind", it)
            }
        }
        val target = when (kind) {
            "telegram" -> state.values["channel.destination"]
            "feishu-app" -> state.values["channel.user_open_id"]
            else -> null
        }?.takeIf { it.isNotBlank() } ?: state.snapshot.settingsObject("channel_status").settingsObject(kind).settingsText("peer")
        if (!target.isNullOrBlank()) SettingsHint(strings.text("接收会话：$target", "Receiving chat: $target"))
        else if (kind !in setOf("none", "feishu-webhook")) SettingsHint(strings.text("等待建立私聊", "Waiting for a private chat"))
        SettingsSummary(strings.text("推送时段", "Delivery times"), state.times.joinToString(" · "), strings) { edit("times") }
    }
}

@Composable
private fun SettingsReviewFields(controller: SettingsController, state: SettingsUiState, strings: Strings) {
        SettingsField(
            controller, state, "review.count", strings.text("每次数量（条）", "Notes per review"),
            keyboard = KeyboardType.Number,
        )
        SettingsField(
            controller, state, "review.min_age_hours", strings.text("等待期（小时）", "Minimum note age (hours)"),
            keyboard = KeyboardType.Number,
        )
        SettingsField(
            controller, state, "review.recent_days", strings.text("重复排除期（天）", "Avoid repeats for (days)"),
            keyboard = KeyboardType.Number,
        )
        SettingsCheckbox(controller, state, "review.include_archived", strings.text("包含归档笔记", "Include archived notes"))
}

@Composable
private fun SettingsTimes(controller: SettingsController, state: SettingsUiState, strings: Strings) {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            state.times.forEachIndexed { index, value ->
                Row(
                    Modifier.fillMaxWidth(),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    SettingsInput(
                        value,
                        { controller.changeTime(index, it) },
                        strings.text("推送时段 ${index + 1}", "Delivery time ${index + 1}"),
                        enabled = !state.busy,
                        keyboard = KeyboardType.Ascii,
                        modifier = Modifier.weight(1f).testTag("settings_time_$index"),
                    )
                    SettingsGhost(
                        strings.text("移除", "Remove"),
                        { controller.removeTime(index) },
                        enabled = !state.busy && state.times.size > 1,
                        modifier = Modifier.testTag("settings_time_remove_$index"),
                    )
                }
            }
            SettingsGhost(
                strings.text("添加时段", "Add time"),
                controller::addTime,
                enabled = !state.busy && state.times.size < 24,
                modifier = Modifier.testTag("settings_time_add"),
            )
            SettingsHint(strings.text(
                "时段数即每日推送次数；每个时段分配不同笔记。",
                "Each time schedules one daily delivery with a different set of notes.",
            ))
        }
}

@Composable
private fun SettingsModels(state: SettingsUiState, strings: Strings, edit: () -> Unit) {
    SettingsSection(strings.text("检索", "Search")) {
        SettingsSummary(strings.text("向量模型", "Embedding model"),
            if (state.values["embedding.enabled"] == "true") state.values["embedding.model"].orEmpty()
            else strings.text("已关闭", "Disabled"), strings, edit)
        val models = state.models
        val building = models.settingsObject("vector_index").settingsObject("building")
        val indexState = models.settingsText("index_status")
        val showProgress = !state.conflict && building != null && indexState in setOf("building", "unavailable")
        val completed = building.settingsText("completed_chunks")?.toLongOrNull() ?: 0L
        val total = building.settingsText("chunks")?.toLongOrNull() ?: 0L
        val label = when {
            state.conflict -> strings.text("索引状态已失效，请重新加载设置。", "Index status is out of date. Reload Settings.")
            models == null -> strings.text("正在读取索引状态…", "Loading index status…")
            indexState == "disabled" -> strings.text("语义索引已关闭", "Semantic index disabled")
            indexState == "waiting" -> strings.text("语义索引等待同步或构建", "Semantic index waiting for sync or build")
            indexState == "building" -> strings.text("语义索引构建中", "Building semantic index")
            indexState == "ready" -> strings.text("语义索引已就绪", "Semantic index ready")
            indexState == "unavailable" -> strings.text("语义索引构建失败，等待重试", "Semantic index build failed; waiting to retry")
            else -> strings.text("索引状态不可用", "Index status unavailable")
        }
        val progressText = if (showProgress) strings.text(
            " · $completed/$total 个片段",
            " · $completed/$total ${if (total == 1L) "chunk" else "chunks"}",
        ) + building.settingsText("last_error")?.takeIf { it.isNotBlank() }?.let { " · $it" }.orEmpty() else ""
        val pollError = state.checkErrors["models"]
        if (pollError != null && !state.conflict) {
            SettingsMessage(settingsMessage(pollError, strings), error = true)
        } else SettingsMessage(label + progressText)
        if (showProgress) LinearProgressIndicator(
            progress = { (completed.toFloat() / total.coerceAtLeast(1L)).coerceIn(0f, 1f) },
            modifier = Modifier.fillMaxWidth().semantics {
                contentDescription = strings.text("语义索引进度", "Semantic index progress")
            },
        )
    }
}

@Composable
private fun SettingsModel(
    controller: SettingsController,
    state: SettingsUiState,
    strings: Strings,
) {
    var advanced by remember { mutableStateOf(false) }
    LaunchedEffect(state.error) { if (state.error != null && !state.conflict) advanced = true }
    SettingsCheckbox(controller, state, "embedding.enabled", strings.text("启用 Embedding", "Enable embeddings"))
    SettingsChoiceField(
        controller, state, "embedding.protocol", strings.text("接口协议", "API protocol"),
        listOf("dashscope" to "DashScope",
            "openai-compatible" to strings.text("OpenAI 兼容 Embedding", "OpenAI compatible embeddings")),
    )
    SettingsField(controller, state, "embedding.base_url", strings.text("API 地址", "API URL"), keyboard = KeyboardType.Uri)
    SettingsField(controller, state, "embedding.model", strings.text("模型名称", "Model name"))
    SettingsSecret(controller, state, strings, "embedding_api_key", "Embedding API Key")
    if (state.snapshot.settingsObject("settings").settingsObject("embedding").settingsBoolean("enabled")) {
        SettingsGhost(strings.text("检查 Embedding 连接", "Check Embedding connection"),
            { controller.checkModel("embedding") }, enabled = controller.canCheckModel("embedding"))
        state.checkErrors["embedding"]?.let { SettingsMessage(settingsMessage(it, strings), error = true) }
    }
    SettingsGhost(strings.text("高级参数", "Advanced settings"), { advanced = !advanced })
    if (!advanced) return
    SettingsField(
        controller, state, "embedding.timeout_seconds", strings.text("请求超时（秒）", "Request timeout (seconds)"),
        keyboard = KeyboardType.Decimal,
    )
    SettingsField(
        controller, state, "embedding.max_concurrency", strings.text("并发数", "Concurrency"),
        keyboard = KeyboardType.Number,
    )
    SettingsField(
        controller, state, "embedding.max_retries", strings.text("重试次数", "Retries"),
        keyboard = KeyboardType.Number,
    )
    val generic = state.values["embedding.protocol"] == "openai-compatible"
    SettingsField(
        controller, state, "embedding.dimensions", strings.text("向量维度", "Vector dimensions"),
        keyboard = KeyboardType.Number,
    )
    if (generic) SettingsCheckbox(
        controller, state, "embedding.send_dimensions", strings.text("发送维度参数", "Send dimensions parameter"),
        hint = strings.text(
            "让模型按你填写的维度生成向量。模型只支持固定维度时可以关闭。",
            "Ask the model to generate vectors at your chosen size. Leave this off for a fixed-size model.",
        ),
    )
    SettingsField(
        controller, state, "embedding.batch_size", strings.text("批量大小", "Batch size"),
        keyboard = KeyboardType.Number,
    )
    if (!generic) SettingsField(
        controller, state, "embedding.query_instruction", strings.text("查询指令", "Query instruction"),
        multiline = true,
        hint = strings.text("在搜索词前加一句任务提示，帮助模型理解你想找什么。",
            "Add a short instruction before the search text to tell the model what to look for."),
        separator = false,
    )
}

private fun channelChoices(strings: Strings) = listOf(
    "none" to strings.text("不推送", "No destination"),
    "qq" to strings.text("QQ 私聊", "QQ private chat"),
    "telegram" to "Telegram",
    "feishu-app" to strings.text("飞书应用机器人", "Feishu app bot"),
    "feishu-webhook" to strings.text("飞书群 Webhook", "Feishu group webhook"),
)

private fun channelLabel(value: String?, strings: Strings) = when (value) {
    "disabled" -> strings.text("未配置", "Not configured")
    "configured" -> strings.text("已配置", "Configured")
    "connecting" -> strings.text("连接中", "Connecting")
    "awaiting_verification" -> strings.text("等待回调验证", "Waiting for callback verification")
    "connected" -> strings.text("已连接", "Connected")
    "disconnected" -> strings.text("连接已断开", "Disconnected")
    "error" -> strings.text("连接失败", "Connection failed")
    "BOT_CREDENTIALS" -> strings.text("请检查机器人凭据。", "Check the bot credentials.")
    "BOT_CONNECTION" -> strings.text("网络连接中断。", "Network connection interrupted.")
    "QQ_SESSION_LOST" -> strings.text("离线期间可能有消息未收到。", "Messages may have been missed while offline.")
    "BOT_RATE_LIMITED" -> strings.text("平台限制了请求频率。", "The platform is rate limiting requests.")
    "BOT_PROTOCOL" -> strings.text("平台响应异常。", "Unexpected platform response.")
    "TELEGRAM_WEBHOOK" -> strings.text("外部 Webhook 已占用收件。", "An external webhook is receiving updates.")
    "BOT_STORAGE" -> strings.text("收件记录写入失败。", "Could not save incoming messages.")
    "STATE_KEY_REQUIRED" -> strings.text("请配置状态加密密钥。", "Configure the state encryption key.")
    "FEISHU_CONNECTION_ERROR" -> strings.text("连接飞书失败。", "Could not connect to Feishu.")
    "pending" -> strings.text("等待发送", "Queued")
    "running" -> strings.text("发送中", "Sending")
    "sent" -> strings.text("已发送", "Sent")
    "failed" -> strings.text("发送失败", "Delivery failed")
    else -> value.orEmpty()
}

@Composable
private fun SettingsChannels(state: SettingsUiState, strings: Strings, edit: (String) -> Unit) {
    val channels = state.snapshot.settingsObject("channel_status")
    val uri = LocalUriHandler.current
    SettingsSection(strings.text("消息渠道", "Messaging channel")) {
        if ("channels" in state.checkErrors) SettingsMessage(strings.text("连接状态不可用", "Connection status unavailable"), error = true)
        for ((kind, secret) in SettingsForm.channelSecrets) {
            val status = channels.settingsObject(kind)
            val configured = state.snapshot.settingsObject("secrets").settingsObject(secret).settingsBoolean("configured")
            val detail = listOf(channelLabel(status.settingsText("state") ?: if (configured) "configured" else "disabled", strings),
                channelLabel(status.settingsText("detail"), strings), status.settingsText("peer").orEmpty())
                .filter { it.isNotBlank() }.joinToString(" · ")
            SettingsSummary(channelLabel(kind, strings), detail, strings) { edit(kind) }
        }
        val telegram = channels.settingsObject("telegram")
        val link = telegram.settingsText("link").orEmpty()
        if (Regex("https://t\\.me/[A-Za-z0-9_]+(?:\\?start=[A-Za-z0-9_-]{1,64})?").matches(link)) {
            SettingsGhost(strings.text("打开机器人", "Open bot"), { runCatching { uri.openUri(link) } },
                modifier = Modifier.testTag("settings_open_telegram"))
        }
    }
}

@Composable
private fun SettingsChannelFields(target: String, controller: SettingsController, state: SettingsUiState, strings: Strings) {
    when (target) {
        "qq" -> {
            SettingsField(controller, state, "channel.qq_app_id", "QQ AppID", keyboard = KeyboardType.Ascii)
            SettingsSecret(controller, state, strings, "qq_app_secret", "QQ AppSecret")
        }
        "telegram" -> {
            SettingsSecret(controller, state, strings, "bot_token", "Telegram Bot Token")
            SettingsField(controller, state, "channel.destination", strings.text("Telegram 接收会话", "Telegram delivery chat"),
                hint = strings.text("留空使用本人私聊，也可填写群或频道的 Chat ID。", "Leave blank for your private chat, or enter a group or channel Chat ID."),
                keyboard = KeyboardType.Ascii)
        }
        "feishu-app" -> {
        SettingsChoiceField(controller, state, "channel.feishu_mode", strings.text("接收方式", "Receive messages via"),
            listOf("websocket" to strings.text("长连接", "WebSocket"), "callback" to strings.text("公网回调", "Public callback")))
        SettingsField(controller, state, "channel.app_id", "App ID")
        SettingsSecret(controller, state, strings, "app_secret", "App Secret")
        SettingsField(controller, state, "channel.user_open_id", strings.text("用户 Open ID", "User Open ID"),
            hint = strings.text("私人应用可留空以绑定首个私聊。多人应用请指定用户。", "Leave blank to bind the first private chat. Specify a user for shared apps."))
        if (state.values["channel.feishu_mode"] == "callback") {
            SettingsSecret(controller, state, strings, "verification_token", "Verification Token")
            SettingsSecret(controller, state, strings, "encrypt_key", "Encrypt Key")
        }
        }
        "feishu-webhook" -> {
        SettingsSecret(controller, state, strings, "webhook_url", "Webhook URL")
        SettingsSecret(controller, state, strings, "webhook_secret", "Webhook Secret")
        SettingsCheckbox(controller, state, "channel.webhook_images", strings.text("推送图片", "Include images"))
        val images = state.values["channel.webhook_images"] == "true"
        if (images) SettingsField(controller, state, "channel.webhook_app_id", "Webhook App ID")
        if (images || state.snapshot.settingsObject("secrets").settingsObject("webhook_app_secret").settingsBoolean("configured")) {
            SettingsSecret(controller, state, strings, "webhook_app_secret", "Webhook App Secret", allowReplace = images)
        }
        }
    }
}

@Composable
private fun SettingsHistory(controller: SettingsController, state: SettingsUiState, strings: Strings) {
    SettingsSection(strings.text("发送记录", "Delivery history")) {
        val items = state.deliveries
        if ("history" in state.checkErrors) SettingsMessage(strings.text("读取发送记录失败。", "Could not load delivery history."), error = true)
        else if (items == null) SettingsHint(strings.text("加载中…", "Loading…"))
        else if (items.isEmpty()) SettingsHint(strings.text("暂无发送记录", "No deliveries yet"))
        items.orEmpty().forEach { item ->
            val kind = channelChoices(strings).firstOrNull { it.first == item.settingsText("channel_kind") }?.second
            val status = item.settingsText("status")
            val id = item.settingsText("id").orEmpty()
            SettingsHint(listOfNotNull(item.settingsText("date"), item.settingsText("clock"), kind,
                channelLabel(status, strings), item.settingsText("last_error")).filter { it.isNotBlank() }.joinToString(" · "))
            if (status == "failed" && id.isNotEmpty()) key(id) {
                SettingsGhost(strings.text("重试", "Retry"), { controller.retryDelivery(id) },
                    enabled = id !in state.resending && !state.busy)
            }
            HorizontalDivider(color = MoeriseColors.Line)
        }
    }
}

@Composable
private fun SettingsSection(title: String, content: @Composable ColumnScope.() -> Unit) {
    Surface(
        modifier = Modifier.fillMaxWidth(),
        color = Color.White,
        shape = RoundedCornerShape(14.dp),
        border = BorderStroke(1.dp, MoeriseColors.Line),
    ) {
        Column(Modifier.padding(start = 18.dp, top = 14.dp, end = 18.dp, bottom = 16.dp)) {
            Text(
                title, fontSize = 16.sp, lineHeight = 25.6.sp, fontWeight = FontWeight.SemiBold,
                modifier = Modifier.padding(bottom = 4.dp).semantics { heading() },
            )
            content()
        }
    }
}

@Composable
private fun SettingsRow(
    label: String,
    hint: String = "",
    separator: Boolean = true,
    content: @Composable ColumnScope.() -> Unit,
) {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 9.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(label, fontSize = 14.sp, lineHeight = 22.4.sp)
            if (hint.isNotBlank()) SettingsHint(hint)
        }
        content()
    }
    if (separator) HorizontalDivider(color = Color(0xFFF0F4F9))
}

@Composable
private fun SettingsField(
    controller: SettingsController,
    state: SettingsUiState,
    path: String,
    label: String,
    hint: String = "",
    keyboard: KeyboardType = KeyboardType.Text,
    multiline: Boolean = false,
    minLines: Int = if (multiline) 3 else 1,
    enabled: Boolean = true,
    separator: Boolean = true,
) {
    SettingsRow(label, hint, separator) {
        SettingsInput(
            state.values[path].orEmpty(), { controller.change(path, it) }, label,
            enabled = enabled && !state.busy,
            keyboard = keyboard, multiline = multiline, minLines = minLines,
            modifier = Modifier.fillMaxWidth().testTag("settings_${path.replace('.', '_')}"),
        )
    }
}

@Composable
private fun SettingsChoiceField(
    controller: SettingsController,
    state: SettingsUiState,
    path: String,
    label: String,
    choices: List<Pair<String, String>>,
) {
    val value = state.values[path].orEmpty()
    val options = if (choices.none { it.first == value }) {
        choices + (value to value.ifEmpty { "—" })
    } else choices
    SettingsRow(label) {
        SettingsSelect(
            value, options, label, enabled = !state.busy,
            tag = "settings_${path.replace('.', '_')}",
        ) { controller.change(path, it) }
    }
}

@Composable
private fun SettingsCheckbox(
    controller: SettingsController,
    state: SettingsUiState,
    path: String,
    label: String,
    hint: String = "",
    enabled: Boolean = true,
    atomic: Boolean = false,
) {
    val haptic = rememberMoeriseHaptic()
    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, fontSize = 14.sp)
            if (hint.isNotBlank()) SettingsHint(hint)
        }
        Switch(checked = (if (atomic) state.atomicValues[path] ?: state.values[path] else state.values[path]) == "true", onCheckedChange = {
            haptic()
            if (atomic) controller.saveAtomic(path, it.toString()) else controller.change(path, it.toString())
        }, enabled = enabled && (!atomic || !state.conflict),
            modifier = Modifier.testTag("settings_${path.replace('.', '_')}").semantics { contentDescription = label })
    }
    HorizontalDivider(color = Color(0xFFF0F4F9))
}

@Composable
private fun SettingsSecret(
    controller: SettingsController,
    state: SettingsUiState,
    strings: Strings,
    name: String,
    label: String,
    separator: Boolean = true,
    allowReplace: Boolean = true,
) {
    val edit = state.secrets[name]
    val action = edit?.action ?: "keep"
    val configured = state.snapshot.settingsObject("secrets").settingsObject(name).settingsBoolean("configured")
    val direct = !configured && !name.endsWith("_api_key")
    SettingsRow(label, separator = separator) {
        if (!direct) Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            SettingsHint(if (configured) strings.text("已配置", "Configured") else strings.text("未配置", "Not configured"))
            SettingsSelect(
                action,
                listOf(
                    "keep" to strings.text("保持", "Keep"),
                    "replace" to strings.text("替换", "Replace"),
                    "clear" to strings.text("清空", "Clear"),
                ).filter { allowReplace || it.first != "replace" },
                strings.text("${label}操作", "$label operation"),
                enabled = !state.busy,
                tag = "settings_secret_${name}_action",
                modifier = Modifier.width(116.dp),
            ) { controller.secret(name, it) }
        }
        if (direct || action == "replace") SettingsInput(
            edit?.value.orEmpty(),
            { controller.secret(name, if (direct && it.isEmpty()) "keep" else "replace", it) },
            strings.text("${label}新值", "New $label value"),
            enabled = !state.busy,
            keyboard = KeyboardType.Password,
            password = true,
            placeholder = strings.text("填写真实的新值", "Enter the new value"),
            autofocus = !direct,
            modifier = Modifier.fillMaxWidth().testTag("settings_secret_${name}_value"),
        )
    }
}

@Composable
private fun SettingsInput(
    value: String,
    onChange: (String) -> Unit,
    label: String,
    enabled: Boolean,
    modifier: Modifier = Modifier,
    keyboard: KeyboardType = KeyboardType.Text,
    multiline: Boolean = false,
    minLines: Int = 1,
    password: Boolean = false,
    placeholder: String = "",
    autofocus: Boolean = false,
) {
    var focused by remember { mutableStateOf(false) }
    val focus = remember { FocusRequester() }
    LaunchedEffect(autofocus) { if (autofocus) focus.requestFocus() }
    BasicTextField(
        value, onChange,
        enabled = enabled,
        singleLine = !multiline,
        minLines = minLines,
        textStyle = MaterialTheme.typography.bodyLarge.copy(
            fontSize = 16.sp, lineHeight = 25.6.sp,
            color = MoeriseColors.Ink.copy(alpha = if (enabled) 1f else .5f),
        ),
        keyboardOptions = KeyboardOptions(keyboardType = keyboard, autoCorrectEnabled = false),
        visualTransformation = if (password) PasswordVisualTransformation() else VisualTransformation.None,
        cursorBrush = SolidColor(MoeriseColors.Blue),
        modifier = modifier.drawerGestureRegion()
            .focusRequester(focus)
            .onFocusChanged { focused = it.isFocused }
            .border(1.dp, if (focused) Color(0xFFB5CCF2) else MoeriseColors.Line, RoundedCornerShape(8.dp))
            .semantics { contentDescription = label },
        decorationBox = { input ->
            Box(
                Modifier.fillMaxWidth().heightIn(min = if (multiline) 90.dp else 44.dp)
                    .padding(horizontal = 10.dp, vertical = 8.dp),
                contentAlignment = Alignment.TopStart,
            ) {
                if (value.isEmpty() && placeholder.isNotEmpty()) Text(
                    placeholder, fontSize = 16.sp, lineHeight = 25.6.sp, color = MoeriseColors.Muted,
                )
                input()
            }
        },
    )
}

@Composable
private fun SettingsSelect(
    value: String,
    options: List<Pair<String, String>>,
    label: String,
    enabled: Boolean,
    tag: String,
    modifier: Modifier = Modifier.fillMaxWidth(),
    onSelect: (String) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    var focused by remember { mutableStateOf(false) }
    LaunchedEffect(enabled) { if (!enabled) expanded = false }
    Box(modifier) {
        Surface(
            color = Color.White,
            shape = RoundedCornerShape(8.dp),
            border = BorderStroke(1.dp, if (focused) Color(0xFFB5CCF2) else MoeriseColors.Line),
            modifier = Modifier.fillMaxWidth().testTag(tag)
                .onFocusChanged { focused = it.hasFocus }
                .semantics { contentDescription = label }
                .clickable(enabled = enabled, role = Role.Button) { expanded = true },
        ) {
            Row(
                Modifier.heightIn(min = 44.dp).padding(horizontal = 10.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Text(
                    options.firstOrNull { it.first == value }?.second ?: value,
                    fontSize = 16.sp, lineHeight = 25.6.sp,
                    color = MoeriseColors.Ink.copy(alpha = if (enabled) 1f else .5f),
                    modifier = Modifier.weight(1f),
                )
                WebIcon(
                    WebIcon.ChevronRight, modifier = Modifier.size(16.dp).rotate(90f),
                    tint = MoeriseColors.Ink.copy(alpha = if (enabled) 1f else .5f),
                )
            }
        }
        DropdownMenu(expanded = expanded, onDismissRequest = { expanded = false },
            modifier = Modifier.platformTestTags()) {
            options.forEach { (choice, title) ->
                DropdownMenuItem(
                    text = { Text(title, fontSize = 16.sp) },
                    modifier = Modifier.semantics { selected = choice == value },
                    enabled = enabled,
                    onClick = { expanded = false; onSelect(choice) },
                )
            }
        }
    }
}

@Composable
private fun SettingsGhost(
    label: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
) {
    OutlinedButton(
        onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp),
        shape = RoundedCornerShape(10.dp),
        border = BorderStroke(1.dp, MoeriseColors.Line),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp),
        colors = ButtonDefaults.outlinedButtonColors(contentColor = MoeriseColors.Muted),
    ) { Text(label, fontSize = 14.sp, lineHeight = 22.4.sp, fontWeight = FontWeight.Normal) }
}

@Composable
private fun SettingsHint(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier = modifier, color = MoeriseColors.Muted, fontSize = 13.sp, lineHeight = 22.1.sp)
}

@Composable
private fun SettingsMessage(text: String, error: Boolean = false) {
    Text(
        text, color = if (error) MaterialTheme.colorScheme.error else MoeriseColors.Muted,
        fontSize = 13.sp, lineHeight = 20.8.sp,
        modifier = Modifier.semantics {
            liveRegion = if (error) LiveRegionMode.Assertive else LiveRegionMode.Polite
        },
    )
}

private fun JsonObject?.settingsObject(key: String): JsonObject? = this?.get(key) as? JsonObject
private fun JsonObject?.settingsText(key: String): String? = (this?.get(key) as? JsonPrimitive)?.contentOrNull
private fun JsonObject?.settingsBoolean(key: String): Boolean = (this?.get(key) as? JsonPrimitive)?.booleanOrNull == true

internal fun settingsMessage(code: String, strings: Strings): String = when (code) {
    "SETTINGS_CONFLICT" -> strings.text(
        "设置已在其他页面更新。重新加载后再编辑。",
        "Settings changed in another page. Reload to continue editing.",
    )
    "SETTINGS_BUSY" ->
        strings.text("设置服务繁忙，请稍后重新保存。", "The settings service is busy. Please save again later.")
    "FEISHU_CALLBACK_CLEAR_REQUIRES_APP_SECRET" ->
        strings.text("清空飞书回调令牌前，请明确选择清空 App Secret，以停用飞书应用机器人。",
            "To clear the Feishu verification token, explicitly choose to clear the App Secret and disable the bot.")
    "INVALID_ARGUMENT", "SETTINGS_INPUT_INVALID" ->
        strings.text("设置不合法，请检查字段、时段和密钥配置。", "Check your settings, delivery times and secrets.")
    "SETTINGS_RESPONSE_INVALID" ->
        strings.text("服务返回的设置不完整，请重新加载。", "The server returned incomplete settings. Reload settings.")
    "SETTINGS_SAVED" -> strings.text("设置已保存。", "Settings saved.")
    "SETTINGS_SAVED_TOMORROW" -> strings.text("已保存，次日生效。", "Saved. Takes effect tomorrow.")
    "SETTINGS_PUSH_STOPPED" -> strings.text("已保存，回顾推送已关闭。", "Saved. Review delivery is off.")
    "EMBEDDING_KEY_ORIGIN" -> strings.text("API 地址更换了域名、协议或端口，请替换或清空密钥。",
        "Replace or clear the key when the API server’s host, scheme or port changes.")
    "SETTINGS_UNCHANGED" ->
        strings.text("没有需要保存的修改。", "No changes to save.")
    "SETTINGS_MAP_REBUILT" ->
        strings.text("已请求重新生成星图。", "Star map rebuild requested.")
    "SETTINGS_MODEL_CONNECTED" -> strings.text("连接正常。", "Connection successful.")
    "EMBEDDING_DISABLED" ->
        strings.text("请先启用并保存 Embedding。", "Enable embeddings and save your settings first.")
    "EMBEDDING_AUTH" -> strings.text("Embedding 密钥被拒绝，请检查密钥和模型权限。", "The embedding API key was rejected. Check the key and model permissions.")
    "EMBEDDING_REJECTED" -> strings.text("Embedding 请求被拒绝，请检查协议、地址、模型和维度。", "The embedding request was rejected. Check the protocol, URL, model and dimensions.")
    "EMBEDDING_PROTOCOL" -> strings.text("Embedding 返回格式或维度不符合配置。", "The embedding response format or dimensions do not match your settings.")
    "EMBEDDING_RATE_LIMITED" -> strings.text("Embedding 调用受限，请稍后重试。", "Embedding requests are rate limited. Please try again later.")
    "EMBEDDING_UNAVAILABLE" -> strings.text("Embedding 连接失败，请检查服务和地址。", "Could not connect to the embedding service. Check the service and URL.")
    "MODEL_CHECK_TIMEOUT", "CHANNEL_CHECK_TIMEOUT" -> strings.text("连接检查超时，请稍后重试。", "The connection check timed out. Please try again later.")
    "BOT_CREDENTIALS", "BOT_CONNECTION", "BOT_RATE_LIMITED", "BOT_PROTOCOL" -> channelLabel(code, strings)
    "CHANNEL_AUTH" -> strings.text("飞书拒绝了应用凭据，请检查 App ID 和 App Secret。",
        "Feishu rejected the credentials. Check the App ID and App Secret.")
    "UPSTREAM_AUTH_UNAVAILABLE", "UPSTREAM_UNAVAILABLE" ->
        strings.text("Memos 连接失败，请恢复笔记服务后重试。", "Could not connect to Memos. Restore the note service and try again.")
    else -> connectionError(code, strings)
}
