package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

object MoeriseColors {
    val Paper = Color(0xFFF7F9FC)
    val Ink = Color(0xFF1F2328)
    val Muted = Color(0xFF5F6672)
    val Line = Color(0xFFE3E8EF)
    val Blue = Color(0xFF1E4A9A)
    val BlueSurface = Color(0xFFDCE8FB)
}

@Composable
fun MoeriseApp(session: SessionController) {
    var content by remember { mutableStateOf("") }
    val strings = remember { Strings(UiLanguage.SYSTEM) }
    val connection by session.state.collectAsState()
    MaterialTheme(colorScheme = lightColorScheme(
        primary = MoeriseColors.Blue, background = MoeriseColors.Paper,
        surface = Color.White, onSurface = MoeriseColors.Ink,
    )) {
        Surface(Modifier.fillMaxSize(), color = MoeriseColors.Paper) {
            if (connection.profile == null || connection.needsLogin) {
                Box(Modifier.safeDrawingPadding().imePadding()) { ConnectionScreen(session, strings) }
                return@Surface
            }
            Column(Modifier.safeDrawingPadding().imePadding().padding(16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text("Moerise", fontSize = 20.sp)
                    TextButton(onClick = session::logout) { Text(strings.text("退出登录", "Sign out")) }
                }
                Text(connection.profile!!.displayName.ifBlank { connection.profile!!.userName }, color = MoeriseColors.Muted)
                connection.error?.let {
                    Text(connectionError(it, strings), color = MaterialTheme.colorScheme.error)
                    TextButton(onClick = session::retrySession) { Text(strings.text("重试连接", "Retry connection")) }
                }
                Surface(shape = RoundedCornerShape(18.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
                    BasicTextField(
                        value = content, onValueChange = { content = it },
                        modifier = Modifier.fillMaxWidth().heightIn(min = 160.dp).padding(20.dp),
                        textStyle = TextStyle(color = MoeriseColors.Ink, fontSize = 17.sp, lineHeight = 30.sp),
                        decorationBox = { inner ->
                            Box {
                                if (content.isEmpty()) Text(strings.text("写下此刻的想法…", "Write what’s on your mind…"), color = MoeriseColors.Muted, fontSize = 17.sp)
                                inner()
                            }
                        },
                    )
                }
            }
        }
    }
}
