package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
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
    var tab by rememberSaveable { mutableIntStateOf(0) }
    val scroll = rememberScrollState()

    LaunchedEffect(controller) { controller.load() }
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
                when {
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
                        WebTabs(
                            listOf(
                                strings.text("通用", "General"),
                                strings.text("回顾", "Review"),
                                strings.text("模型", "Models"),
                                strings.text("洞察", "Insights"),
                                strings.text("消息渠道", "Messaging channel"),
                            ),
                            tab, { if (!state.busy) tab = it },
                            tagPrefix = "settings_tab",
                            enabled = !state.busy,
                        )
                        when (tab) {
                            0 -> SettingsSection(strings.text("通用", "General")) {
                                SettingsRow(strings.text("语言", "Language"), separator = false) {
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
                                SettingsCheckbox(controller, state, "stargazing.enabled",
                                    strings.text("启用观星", "Enable stargazing"))
                                if (preferencesError != null) SettingsMessage(
                                    strings.text(
                                        "语言设置未能保存，请重新选择。",
                                        "Could not save the language setting. Select it again.",
                                    ), error = true,
                                )
                            }
                            1 -> SettingsReview(controller, state, strings)
                            2 -> SettingsModels(controller, state, strings)
                            3 -> SettingsSection(strings.text("默认洞察 Prompt", "Default insight prompt")) {
                                SettingsField(
                                    controller, state, "insights.default_prompt",
                                    strings.text("一键洞察使用的 Prompt", "Prompt for Generate insight"),
                                    multiline = true, minLines = 7,
                                )
                                SettingsGhost(
                                    strings.text("恢复默认", "Restore default"),
                                    controller::restorePrompt,
                                    enabled = !state.busy &&
                                        state.snapshot.settingsText("default_insight_prompt") != null,
                                    modifier = Modifier.testTag("settings_restore_prompt"),
                                )
                            }
                            4 -> SettingsChannel(controller, state, strings)
                        }
                        Column(
                            Modifier.fillMaxWidth().padding(top = 4.dp, bottom = 12.dp),
                            verticalArrangement = Arrangement.spacedBy(8.dp),
                        ) {
                            val error = if (state.conflict) "SETTINGS_CONFLICT" else state.error
                            error?.let { SettingsMessage(settingsMessage(it, strings), error = true) }
                            state.status?.takeIf { it.isNotBlank() }?.let {
                                SettingsMessage(settingsMessage(it, strings))
                            }
                            Row(
                                Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.spacedBy(8.dp),
                                verticalAlignment = Alignment.CenterVertically,
                            ) {
                                Box(Modifier.weight(1f)) {
                                    SettingsGhost(
                                        strings.text("重新加载设置", "Reload settings"),
                                        controller::load,
                                        enabled = !state.busy,
                                        modifier = Modifier.testTag("settings_reload"),
                                    )
                                }
                                WebAction(
                                    strings.text("保存设置", "Save settings"),
                                    controller::save,
                                    enabled = !state.busy && !state.conflict,
                                    modifier = Modifier.testTag("settings_save"),
                                )
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun SettingsReview(controller: SettingsController, state: SettingsUiState, strings: Strings) {
    SettingsSection(strings.text("回顾", "Review")) {
        SettingsField(
            controller, state, "timezone", strings.text("时区", "Timezone"),
            hint = strings.text(
                "使用 IANA 名称，例如 Asia/Hong_Kong。",
                "Use an IANA name, such as Europe/London.",
            ),
        )
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
        SettingsCheckbox(controller, state, "notifications.enabled", strings.text("开启回顾推送", "Enable review delivery"))
        SettingsSubheading(strings.text("推送时段", "Delivery times"))
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
        SettingsHint(
            strings.text(
                "回顾参数、时段和时区从后续日期生效，已固定的当天计划保持不变。",
                "Review settings, times and timezone apply to future days. Today's planned review stays the same.",
            ),
            Modifier.padding(top = 10.dp),
        )
    }
}

@Composable
private fun SettingsModels(controller: SettingsController, state: SettingsUiState, strings: Strings) {
    SettingsSection(strings.text("模型", "Models")) {
        SettingsModel(controller, state, strings, "llm", "LLM")
        HorizontalDivider(Modifier.padding(top = 16.dp), color = MoeriseColors.Line)
        SettingsModel(controller, state, strings, "embedding", "Embedding")
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
    group: String,
    title: String,
) {
    SettingsSubheading(title)
    SettingsCheckbox(controller, state, "$group.enabled", strings.text("启用 $title", "Enable $title"))
    if (group == "embedding") SettingsChoiceField(
        controller, state, "embedding.provider", strings.text("供应商", "Provider"),
        listOf("bailian" to strings.text("百炼", "Bailian"), "openai-compatible" to strings.text("OpenAI 兼容", "OpenAI compatible")),
    )
    SettingsChoiceField(
        controller, state, "$group.protocol", strings.text("接口协议", "API protocol"),
        if (group == "llm") listOf(
            "openai-compatible" to "OpenAI Chat Completions",
            "openai-responses" to "OpenAI Responses",
            "anthropic" to "Anthropic",
        ) else listOf(
            "dashscope" to "DashScope",
            "openai-compatible" to strings.text("OpenAI 兼容 Embedding", "OpenAI compatible embeddings"),
        ),
    )
    SettingsField(controller, state, "$group.base_url", strings.text("API 地址", "API URL"), keyboard = KeyboardType.Uri)
    SettingsField(controller, state, "$group.model", strings.text("模型名称", "Model name"))
    SettingsSecret(controller, state, strings, "${group}_api_key", "$title API Key")
    Text(
        strings.text("高级参数", "Advanced settings"),
        color = MoeriseColors.Muted,
        fontSize = 13.sp,
        lineHeight = 20.8.sp,
        fontWeight = FontWeight.Medium,
        modifier = Modifier.padding(top = 16.dp, bottom = 2.dp).semantics { heading() },
    )
    SettingsField(
        controller, state, "$group.timeout_seconds", strings.text("请求超时（秒）", "Request timeout (seconds)"),
        keyboard = KeyboardType.Decimal,
    )
    SettingsField(
        controller, state, "$group.max_concurrency", strings.text("并发数", "Concurrency"),
        keyboard = KeyboardType.Number,
    )
    SettingsField(
        controller, state, "$group.max_retries", strings.text("重试次数", "Retries"),
        keyboard = KeyboardType.Number,
    )
    if (group == "llm") {
        SettingsField(
            controller, state, "llm.max_output_tokens", strings.text("最大输出 Token", "Maximum output tokens"),
            keyboard = KeyboardType.Number,
            separator = false,
        )
    } else {
        val generic = state.values["embedding.protocol"] == "openai-compatible"
        SettingsField(
            controller, state, "embedding.dimensions", strings.text("向量维度", "Vector dimensions"),
            keyboard = KeyboardType.Number,
        )
        SettingsCheckbox(
            controller, state, "embedding.send_dimensions", strings.text("发送维度参数", "Send dimensions parameter"),
            enabled = generic,
            hint = strings.text(
                "通用协议按需开启；固定维度模型可关闭，返回维度仍须一致。",
                "Enable if your compatible API needs it. Fixed-dimension models can omit it; returned dimensions must still match.",
            ),
        )
        SettingsField(
            controller, state, "embedding.batch_size", strings.text("批量大小", "Batch size"),
            keyboard = KeyboardType.Number,
        )
        SettingsField(
            controller, state, "embedding.query_instruction", strings.text("查询指令", "Query instruction"),
            multiline = true,
            enabled = !generic,
            hint = strings.text("仅百炼 DashScope 支持。", "Supported only by Bailian DashScope."),
            separator = false,
        )
    }
    val model = state.models.settingsObject(group)
    SettingsHint(when {
        state.conflict -> strings.text("设置已变更，请重新加载。", "Settings changed. Please reload.")
        model == null -> strings.text("正在读取模型状态…", "Loading model status…")
        !model.settingsBoolean("enabled") -> strings.text("已关闭", "Disabled")
        model.settingsBoolean("configured") -> strings.text("已启用 · 已配置", "Enabled · Configured")
        else -> strings.text("配置不完整", "Configuration incomplete")
    })
    val dirty = controller.modelDirty(group)
    val checking = group in state.checking
    SettingsGhost(
        strings.text("检查 $title 连接", "Check $title connection"),
        { controller.checkModel(group) },
        enabled = controller.canCheckModel(group),
        modifier = Modifier.testTag("settings_${group}_check"),
    )
    when {
        state.conflict -> Unit
        dirty -> SettingsMessage(strings.text("模型设置有修改，请保存后检查。", "Model settings changed. Save before checking."))
        checking -> SettingsMessage(strings.text("正在检查已保存配置…", "Checking saved configuration…"))
        state.checkErrors[group] != null -> SettingsMessage(
            settingsMessage(state.checkErrors.getValue(group), strings), error = true,
        )
        state.checks[group] != null -> {
            val duration = state.checks[group].settingsText("duration_ms").orEmpty()
            SettingsMessage(strings.text("连接正常 · $duration 毫秒", "Connected · $duration ms"))
        }
    }
    SettingsHint(strings.text(
        "连接检查使用已保存配置，会产生少量模型用量。",
        "Connection checks use saved settings and make a small model request.",
    ))
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun SettingsChannel(controller: SettingsController, state: SettingsUiState, strings: Strings) {
    SettingsSection(strings.text("消息渠道", "Messaging channel")) {
        val kind = state.values["channel.kind"].orEmpty()
        SettingsChoiceField(
            controller, state, "channel.kind", strings.text("发送方式", "Delivery method"),
            listOf(
                "none" to strings.text("不启用消息渠道", "No messaging channel"),
                "feishu-app" to strings.text("飞书应用机器人", "Feishu app bot"),
                "feishu-webhook" to strings.text("飞书 Webhook", "Feishu webhook"),
                "telegram" to "Telegram",
            ),
        )
        val capabilities = state.snapshot.settingsObject("channel_capabilities")
        FlowRow(
            Modifier.fillMaxWidth().padding(top = 12.dp, bottom = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            listOf(
                "text" to strings.text("文字推送", "Text delivery"),
                "images" to strings.text("图文推送", "Text and images"),
                "record" to strings.text("双向记录", "Note capture"),
            ).forEach { (key, label) ->
                val available = capabilities.settingsBoolean(key)
                val text = label + strings.text("：", ": ") +
                    if (available) strings.text("可用", "Available") else strings.text("不可用", "Unavailable")
                if (available) Surface(
                    color = MoeriseColors.TagSurface,
                    border = BorderStroke(1.dp, MoeriseColors.TagLine),
                    shape = RoundedCornerShape(50),
                ) {
                    Text(
                        text, color = Color(0xFF2A5BB5), fontSize = 13.sp, lineHeight = 20.8.sp,
                        modifier = Modifier.padding(horizontal = 9.dp, vertical = 1.dp),
                    )
                } else SettingsHint(text)
            }
        }
        when {
            kind == "none" -> SettingsHint(strings.text(
                "选择消息渠道后可开启回顾推送，也可以只在 Web 阅读。",
                "Choose a messaging channel to receive scheduled reviews, or read them in the Web app.",
            ))
            kind.startsWith("feishu-") -> {
                SettingsHint(strings.text(
                    "仅配置 Webhook URL 可发送文字。图文还需 App ID 和 App Secret；双向记录还需 Verification Token 和用户 Open ID。能力在保存后更新。",
                    "A webhook URL supports text delivery. Images also need an App ID and App Secret. Note capture additionally needs a Verification Token and user Open ID. Save to update available features.",
                ))
                SettingsField(controller, state, "channel.app_id", "App ID")
                SettingsSecret(controller, state, strings, "app_secret", "App Secret")
                SettingsSecret(controller, state, strings, "verification_token", "Verification Token")
                SettingsSecret(controller, state, strings, "encrypt_key", "Encrypt Key")
                SettingsField(controller, state, "channel.user_open_id", strings.text("用户 Open ID", "User Open ID"))
                SettingsSecret(controller, state, strings, "webhook_url", "Webhook URL")
                SettingsSecret(controller, state, strings, "webhook_secret", "Webhook Secret", separator = false)
            }
            kind == "telegram" -> {
                SettingsHint(strings.text(
                    "通过 @BotFather 创建机器人，向它发送 /start，再从 getUpdates 获取自己的数字 Chat ID。支持英文回顾和图片原文件；无需公开回调地址。",
                    "Create a bot with @BotFather, send /start to your bot, then find your numeric Chat ID in getUpdates. Reviews use English and include images as original files. No public callback URL is needed.",
                ))
                SettingsSecret(controller, state, strings, "bot_token", "Telegram Bot Token", separator = false)
            }
        }
        if (kind in setOf("feishu-app", "telegram")) SettingsField(
            controller, state, "channel.destination", strings.text("推送目标", "Delivery destination"),
            hint = strings.text(
                "Telegram 填写数字 Chat ID；飞书应用可留空，使用绑定用户的 Open ID。",
                "For Telegram, enter a numeric Chat ID. For a Feishu app, leave blank to use the bound user's Open ID.",
            ),
            keyboard = if (kind == "telegram") KeyboardType.Ascii else KeyboardType.Text,
            separator = false,
        )
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
private fun SettingsSubheading(title: String) {
    Text(
        title, fontSize = 14.sp, lineHeight = 22.4.sp, fontWeight = FontWeight.SemiBold,
        modifier = Modifier.padding(top = 12.dp, bottom = 4.dp).semantics { heading() },
    )
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
) {
    SettingsRow(label, hint) {
        val checked = state.values[path] == "true"
        Box(
            modifier = Modifier.testTag("settings_${path.replace('.', '_')}")
                .fillMaxWidth().heightIn(min = 44.dp)
                .semantics { contentDescription = label }
                .toggleable(
                    value = checked, enabled = enabled && !state.busy, role = Role.Checkbox,
                    onValueChange = { controller.change(path, it.toString()) },
                ),
            contentAlignment = Alignment.CenterStart,
        ) {
            Checkbox(
                checked = checked, onCheckedChange = null, enabled = enabled && !state.busy,
                modifier = Modifier.size(18.dp),
                colors = CheckboxDefaults.colors(checkedColor = Color(0xFF5B8DEF)),
            )
        }
    }
}

@Composable
private fun SettingsSecret(
    controller: SettingsController,
    state: SettingsUiState,
    strings: Strings,
    name: String,
    label: String,
    separator: Boolean = true,
) {
    val edit = state.secrets[name]
    val action = edit?.action ?: "keep"
    val configured = state.snapshot.settingsObject("secrets").settingsObject(name).settingsBoolean("configured")
    SettingsRow(label, separator = separator) {
        Row(
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
                ),
                strings.text("${label}操作", "$label operation"),
                enabled = !state.busy,
                tag = "settings_secret_${name}_action",
                modifier = Modifier.width(116.dp),
            ) { controller.secret(name, it) }
        }
        if (action == "replace") SettingsInput(
            edit?.value.orEmpty(),
            { controller.secret(name, "replace", it) },
            strings.text("${label}新值", "New $label value"),
            enabled = !state.busy,
            keyboard = KeyboardType.Password,
            password = true,
            placeholder = strings.text("填写真实的新值", "Enter the new value"),
            autofocus = true,
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
        modifier = modifier
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
    TextButton(
        onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp),
        shape = RoundedCornerShape(10.dp),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp),
        colors = ButtonDefaults.textButtonColors(contentColor = MoeriseColors.Muted),
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

private fun settingsMessage(code: String, strings: Strings): String = when (code) {
    "SETTINGS_CONFLICT" -> strings.text(
        "设置已在其他页面更新。重新加载后再编辑；重新加载会丢弃表单中的修改。",
        "Settings changed in another page. Reload to edit; this discards your unsaved changes.",
    )
    "SETTINGS_BUSY" ->
        strings.text("设置服务繁忙，请稍后重新保存。", "The settings service is busy. Please save again later.")
    "INVALID_ARGUMENT", "SETTINGS_INPUT_INVALID" ->
        strings.text("设置不合法，请检查字段、时段和密钥配置。", "Check your settings, delivery times and secrets.")
    "SETTINGS_RESPONSE_INVALID" ->
        strings.text("服务返回的设置不完整，请重新加载。", "The server returned incomplete settings. Reload settings.")
    "SETTINGS_SAVED" -> strings.text("设置已保存。", "Settings saved.")
    "SETTINGS_UNCHANGED" ->
        strings.text("没有需要保存的修改。", "No changes to save.")
    "SETTINGS_PROMPT_RESTORED" ->
        strings.text("已恢复默认 Prompt，保存后生效。", "Default prompt restored. Save to apply.")
    "LLM_DISABLED" -> strings.text("请先启用并保存 LLM。", "Enable the LLM and save your settings first.")
    "EMBEDDING_DISABLED" ->
        strings.text("请先启用并保存 Embedding。", "Enable embeddings and save your settings first.")
    "LLM_AUTH" -> strings.text("LLM 密钥被拒绝，请检查密钥和模型权限。", "The LLM API key was rejected. Check the key and model permissions.")
    "EMBEDDING_AUTH" -> strings.text("Embedding 密钥被拒绝，请检查密钥和模型权限。", "The embedding API key was rejected. Check the key and model permissions.")
    "LLM_REJECTED" -> strings.text("LLM 请求被拒绝，请检查协议、地址和模型。", "The LLM request was rejected. Check the protocol, URL and model.")
    "EMBEDDING_REJECTED" -> strings.text("Embedding 请求被拒绝，请检查协议、地址、模型和维度。", "The embedding request was rejected. Check the protocol, URL, model and dimensions.")
    "LLM_PROTOCOL" -> strings.text("LLM 返回格式不符合所选协议。", "The LLM response does not match the selected protocol.")
    "EMBEDDING_PROTOCOL" -> strings.text("Embedding 返回格式或维度不符合配置。", "The embedding response format or dimensions do not match your settings.")
    "LLM_RATE_LIMITED" -> strings.text("LLM 调用受限，请稍后重试。", "LLM requests are rate limited. Please try again later.")
    "EMBEDDING_RATE_LIMITED" -> strings.text("Embedding 调用受限，请稍后重试。", "Embedding requests are rate limited. Please try again later.")
    "LLM_UNAVAILABLE" -> strings.text("LLM 连接失败，请检查服务和地址。", "Could not connect to the LLM service. Check the service and URL.")
    "EMBEDDING_UNAVAILABLE" -> strings.text("Embedding 连接失败，请检查服务和地址。", "Could not connect to the embedding service. Check the service and URL.")
    "MODEL_CHECK_TIMEOUT" -> strings.text("连接检查超时，请稍后重试。", "The connection check timed out. Please try again later.")
    "UPSTREAM_AUTH_UNAVAILABLE", "UPSTREAM_UNAVAILABLE" ->
        strings.text("Memos 连接失败，请恢复笔记服务后重试。", "Could not connect to Memos. Restore the note service and try again.")
    else -> connectionError(code, strings)
}
