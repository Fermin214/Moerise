package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.background
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

@Composable
fun WebSidebar(
    page: String, strings: Strings, userName: String, filters: SearchState, month: String,
    onClose: () -> Unit, onNavigate: (String) -> Unit,
    onMonth: (String) -> Unit, onDate: (String) -> Unit, onTag: (String) -> Unit,
    onLogout: () -> Unit, selectedTags: Set<String> = emptySet(), selectedDate: String = "",
    today: String = "", stargazingEnabled: Boolean = true,
) {
    val scroll = rememberScrollState()
    Column(Modifier.fillMaxHeight().safeDrawingPadding().padding(12.dp)) {
        Row(Modifier.fillMaxWidth().height(44.dp).padding(start = 8.dp),
            verticalAlignment = Alignment.CenterVertically) {
            Text("Moerise", fontSize = 17.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.weight(1f))
            IconButton(onClick = onClose, modifier = Modifier.size(44.dp)) {
                WebIcon(WebIcon.Close, strings.text("关闭导航", "Close navigation"))
            }
        }
            Column(Modifier.weight(1f).fillMaxWidth().testTag("sidebar_scroll").verticalScroll(scroll),
                verticalArrangement = Arrangement.spacedBy(16.dp)) {
                SidebarCalendar(month, filters.calendar, strings, onMonth, onDate, selectedDate, today)
                Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    listOf(
                        Triple("home", strings.text("主页", "Home"), WebIcon.Home),
                        Triple("review", strings.text("回顾", "Review"), WebIcon.Review),
                        Triple("walk", strings.text("漫步", "Walk"), WebIcon.Walk),
                        Triple("map", strings.text("观星", "Stargazing"), WebIcon.Map),
                        Triple("archive", strings.text("归档笔记", "Archived notes"), WebIcon.Archive),
                    ).filter { it.first != "map" || stargazingEnabled }.forEach { (target, label, icon) ->
                        SidebarEntry(label, icon, page == target, Modifier.testTag("nav_$target")) { onNavigate(target) }
                    }
                }
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(strings.text("标签", "Tags"), fontSize = 12.sp, color = MoeriseColors.Muted,
                        modifier = Modifier.padding(start = 10.dp))
                    filters.tags.sortedWith(compareByDescending<TagCount> { it.tag in selectedTags }.thenByDescending { it.count }).forEach { tag ->
                        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(10.dp))
                            .background(if (tag.tag in selectedTags) MoeriseColors.Selected else Color.Transparent)
                            .clickable(role = Role.Button) { onTag(tag.tag) }.heightIn(min = 44.dp).padding(horizontal = 10.dp),
                            verticalAlignment = Alignment.CenterVertically) {
                            Text("#${tag.tag.removePrefix("#")}", fontSize = 13.sp,
                                modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(tag.count.toString(), fontSize = 12.sp, color = MoeriseColors.Muted)
                        }
                    }
                    if (filters.filtersLoading && filters.tags.isEmpty()) LinearProgressIndicator(
                        Modifier.fillMaxWidth().padding(horizontal = 10.dp).height(2.dp))
                    filters.filtersError?.let {
                        Text(searchError(it, strings), fontSize = 12.sp, color = MaterialTheme.colorScheme.error,
                            modifier = Modifier.padding(horizontal = 10.dp))
                    }
                }
            }
            Column(Modifier.fillMaxWidth().background(MoeriseColors.Paper).testTag("sidebar_account")) {
                HorizontalDivider(color = MoeriseColors.Line)
                Column(Modifier.padding(top = 8.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    SidebarEntry(strings.text("设置", "Settings"), WebIcon.Settings, page == "settings",
                        Modifier.testTag("nav_settings")) { onNavigate("settings") }
                    Row(Modifier.fillMaxWidth().padding(start = 10.dp),
                        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        WebIcon(WebIcon.User)
                        Text(userName.removePrefix("users/"), fontSize = 14.sp, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f))
                        TextButton(onClick = onLogout, modifier = Modifier.testTag("account_logout")) {
                            Text(strings.text("退出登录", "Sign out"))
                        }
                    }
                }
            }
    }
}

@Composable
internal fun LogoutDialog(
    state: LogoutState, busy: Boolean, strings: Strings, onConfirm: (Boolean) -> Unit, onDismiss: () -> Unit,
) {
    var clearLocal by remember(state.account) { mutableStateOf(false) }
    val counts = state.counts
    AlertDialog(
        onDismissRequest = onDismiss, modifier = Modifier.platformTestTags(),
        title = { Text(if (counts == null) strings.text("退出登录？", "Sign out?")
            else strings.text("确认清除本地数据并退出？", "Delete local data and sign out?")) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (counts == null) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(clearLocal, { clearLocal = it }, enabled = !busy, modifier = Modifier.testTag("logout_clear_local"))
                        Text(strings.text(
                            "同时清除此设备上的本地数据（含未同步待发笔记、草稿、冲突副本与缓存，不影响服务器已保存笔记）",
                            "Also clear local data on this device, including queued notes, drafts, conflict copies and cache. Notes saved on the server are kept.",
                        ))
                    }
                } else {
                    Text(strings.text("待发笔记 ${counts.queued} 条、冲突副本 ${counts.conflicts} 条、暂存附件 ${counts.attachments} 个。",
                        "${counts.queued} queued notes, ${counts.conflicts} conflict copies, ${counts.attachments} staged attachments."),
                        modifier = Modifier.testTag("logout_cleanup_counts"))
                }
                state.error?.let {
                    Text(if (it == "LOCAL_REMOVE_FAILED") editorError(it, strings)
                        else strings.text("退出登录失败，请重试。", "Could not sign out. Try again."), color = MaterialTheme.colorScheme.error)
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { onConfirm(clearLocal || counts != null) }, enabled = !busy,
                modifier = Modifier.testTag("logout_confirm")) {
                Text(when {
                    counts != null -> strings.text("清除并退出", "Delete and sign out")
                    clearLocal -> strings.text("确认清除并退出", "Confirm deletion and sign out")
                    else -> strings.text("退出登录", "Sign out")
                }, color = if (counts != null) MaterialTheme.colorScheme.error else Color.Unspecified)
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss, enabled = !busy) { Text(strings.text("取消", "Cancel")) }
        },
    )
}

@Composable
private fun SidebarEntry(
    label: String, icon: WebIcon, selected: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit,
) {
    Row(modifier.fillMaxWidth().clip(RoundedCornerShape(10.dp))
        .background(if (selected) MoeriseColors.Selected else Color.Transparent)
        .clickable(role = Role.Button, onClick = onClick).heightIn(min = 44.dp).padding(horizontal = 10.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        val color = if (selected) MoeriseColors.Blue else Color(0xFF3A4049)
        WebIcon(icon, tint = color)
        Text(label, fontSize = 14.sp, fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal, color = color)
    }
}

@Composable
private fun SidebarCalendar(
    month: String, calendar: CalendarPage?, strings: Strings, onMonth: (String) -> Unit, onDate: (String) -> Unit,
    selectedDate: String, today: String,
) {
    val grid = monthGrid(month)
    val counts = calendar?.days.orEmpty().associate { it.date to it.count }
    val heading = strings.text("${grid.year}年${grid.month}月", "${grid.year}-${grid.month.toString().padStart(2, '0')}")
    Column(Modifier.padding(horizontal = 4.dp), verticalArrangement = Arrangement.spacedBy(3.dp)) {
        Row(Modifier.fillMaxWidth().heightIn(min = 32.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(heading, fontSize = 13.sp, fontWeight = FontWeight.SemiBold,
                modifier = Modifier.weight(1f).padding(start = 6.dp))
            IconButton(onClick = { onMonth(shiftMonth(month, -1)) }, modifier = Modifier.size(44.dp)) {
                WebIcon(WebIcon.ChevronLeft, strings.text("上个月", "Previous month"), Modifier.size(16.dp))
            }
            IconButton(onClick = { onMonth(shiftMonth(month, 1)) }, modifier = Modifier.size(44.dp)) {
                WebIcon(WebIcon.ChevronRight, strings.text("下个月", "Next month"), Modifier.size(16.dp))
            }
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(3.dp)) {
            strings.text("一,二,三,四,五,六,日", "M,T,W,T,F,S,S").split(',').forEach {
                Box(Modifier.weight(1f).height(18.dp), contentAlignment = Alignment.Center) {
                    Text(it, fontSize = 11.sp, color = Color(0xFF8A909A), lineHeight = 18.sp)
                }
            }
        }
        val cells = ((grid.offset + grid.days + 6) / 7) * 7
        Column(Modifier.motionEnter(month, distance = -5f), verticalArrangement = Arrangement.spacedBy(3.dp)) {
        for (row in 0 until cells / 7) Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(3.dp)) {
            for (column in 0..6) {
                val day = row * 7 + column - grid.offset + 1
                val valid = day in 1..grid.days
                val date = "$month-${day.toString().padStart(2, '0')}"
                val count = counts[date] ?: 0
                Surface(Modifier.weight(1f).aspectRatio(1f)
                    .then(if (valid) Modifier.clickable { onDate(date) }.semantics {
                        selected = selectedDate == date
                        contentDescription = "$date, $count " + strings.text("条笔记", "notes")
                    } else Modifier),
                    shape = RoundedCornerShape(6.dp),
                    border = if (valid && date == today) BorderStroke(1.dp, Color(0xFF5B8DEF)) else null,
                    color = if (selectedDate == date) MoeriseColors.Blue else if (count > 1) Color(0xFFA9C4F0)
                        else if (count > 0) MoeriseColors.BlueSurface else Color.Transparent) {
                    Box(contentAlignment = Alignment.Center) {
                        if (valid) Text(day.toString(), fontSize = 11.sp,
                            color = if (selectedDate == date) Color.White else if (count > 0) MoeriseColors.Blue else Color(0xFF8A909A),
                            fontWeight = if (date == today) FontWeight.Bold else FontWeight.Normal)
                    }
                }
            }
        }
        }
    }
}
