package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.graphics.vector.PathParser
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

enum class WebIcon {
    Menu,
    Search,
    Plus,
    Hash,
    Download,
    Close,
    Home,
    Review,
    Walk,
    Map,
    Settings,
    User,
    More,
    ChevronLeft,
    ChevronRight,
    Archive,
    Refresh,
    Sync,
    Check,
    CloudOff,
    Alert,
    SignIn,
    ArrowDown,
    Eye,
    EyeOff,
}

/**
 * 使用 Web 的 24 × 24 坐标、1.5 描边和圆角端点；空描述不添加图像语义。
 * 外部尺寸修饰符可覆盖默认的 18.dp，非正方形区域内按比例居中绘制。
 */
@Composable
fun WebIcon(
    icon: WebIcon,
    description: String? = null,
    modifier: Modifier = Modifier,
    tint: Color = MoeriseColors.Muted,
) {
    val path = remember(icon) { PathParser().parsePathString(icon.svgPath()).toPath() }
    val stroke = remember(icon) { Stroke(width = if (icon == WebIcon.More) 2.8f else 1.5f, cap = StrokeCap.Round, join = StrokeJoin.Round) }
    val accessibility = if (description == null) Modifier else Modifier.semantics {
        contentDescription = description
        role = Role.Image
    }

    Canvas(modifier.size(18.dp).then(accessibility)) {
        val side = minOf(size.width, size.height)
        if (side <= 0f) return@Canvas
        withTransform({
            translate((size.width - side) / 2f, (size.height - side) / 2f)
            scale(side / 24f, side / 24f, pivot = Offset.Zero)
        }) {
            when (icon) {
                WebIcon.Search -> drawCircle(
                    tint, radius = 6.2f, center = Offset(10.8f, 10.8f), style = stroke,
                )
                WebIcon.Walk -> {
                    drawCircle(tint, radius = 2f, center = Offset(6.5f, 17.5f), style = stroke)
                    drawCircle(tint, radius = 2f, center = Offset(17.5f, 6.5f), style = stroke)
                }
                WebIcon.Map -> {
                    drawCircle(tint, radius = 1.4f, center = Offset(17.8f, 17f))
                    drawCircle(tint, radius = 1.0f, center = Offset(17.8f, 5.5f))
                }
                WebIcon.Archive -> drawRoundRect(tint, topLeft = Offset(4f, 4.5f), size = Size(16f, 4f), cornerRadius = CornerRadius(1.5f, 1.5f), style = stroke)
                WebIcon.Settings -> {
                    drawCircle(tint, radius = 2f, center = Offset(11.5f, 8f), style = stroke)
                    drawCircle(tint, radius = 2f, center = Offset(15.5f, 16f), style = stroke)
                }
                WebIcon.User -> drawCircle(
                    tint, radius = 3.5f, center = Offset(12f, 8f), style = stroke,
                )
                else -> Unit
            }
            drawPath(path, tint, style = stroke)
        }
    }
}

// 路径源于 web/src/index.html 的 .i SVG；Refresh 使用 web/src/map.js 的刷新路径。
private fun WebIcon.svgPath(): String = when (this) {
    WebIcon.Menu -> "M4.5 9h15M4.5 15h9.5"
    WebIcon.Search -> "M15.6 15.6l4 4"
    WebIcon.Plus -> "M12 5v14M5 12h14"
    WebIcon.Hash -> "M6 9h14M4 15h14M11 4 7 20M17 4l-4 16"
    WebIcon.Download -> "M12 3v12M7 10l5 5 5-5M4 16v4h16v-4"
    WebIcon.Close -> "m6 6 12 12M6 18 18 6"
    WebIcon.Home -> "M5 7h14M5 12h14M5 17h8.5"
    WebIcon.Review -> "M4.5 6.5C7 5.5 10 5.8 12 7.2C14 5.8 17 5.5 19.5 6.5V18C17 17 14 17.3 12 18.7C10 17.3 7 17 4.5 18ZM12 7.2V18.5"
    WebIcon.Walk -> "M8.5 17.5H13.5C15.8 17.5 15.8 12 12 12C8.2 12 8.2 6.5 10.5 6.5H15.5"
    WebIcon.Map -> "M11 4.5Q11 10.5 17 10.5Q11 10.5 11 16.5Q11 10.5 5 10.5Q11 10.5 11 4.5Z"
    WebIcon.Settings -> "M4.5 8h5M13.5 8h6M4.5 16h9M17.5 16h2"
    WebIcon.User -> "M5 20c1-4 4-5.5 7-5.5s6 1.5 7 5.5"
    WebIcon.More -> "M5 12h.01M12 12h.01M19 12h.01"
    WebIcon.ChevronLeft -> "M14 6l-6 6 6 6"
    WebIcon.ChevronRight -> "M10 6l6 6-6 6"
    WebIcon.ArrowDown -> "M12 5v14M6 13l6 6 6-6"
    WebIcon.Archive -> "M5.5 8.5v9a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2v-9M10 13h4"
    WebIcon.Refresh -> "M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1"
    WebIcon.Sync -> "M7.5 18h9.2a4.3 4.3 0 0 0 .5-8.57A5.5 5.5 0 0 0 6.5 8.1 3.8 3.8 0 0 0 7.5 18Z"
    WebIcon.Check -> "M5 12l4 4L19 6"
    WebIcon.CloudOff -> "M3 3l18 18M7 7a6 6 0 0 1 10.3 3.2A4 4 0 0 1 20 17M5 10a4 4 0 0 0 1 8h10"
    WebIcon.Alert -> "M12 8v5M12 17h.01M10.3 3.8L2.8 18a2 2 0 0 0 1.7 3h15a2 2 0 0 0 1.7-3L13.7 3.8a2 2 0 0 0-3.4 0"
    WebIcon.SignIn -> "M14 4h6v16h-6M3 12h12M10 7l5 5-5 5"
    WebIcon.Eye -> "M2 12s3.6-6 10-6 10 6 10 6-3.6 6-10 6S2 12 2 12Z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z"
    WebIcon.EyeOff -> "M3 3l18 18M10.6 6.1c6.9-.8 11.4 5.9 11.4 5.9a17 17 0 0 1-3 3.3M14 17.8C6.4 19.3 2 12 2 12a18 18 0 0 1 4.1-4.1"
}

@Composable
fun MoeriseBrandMark(size: Dp = 52.dp, modifier: Modifier = Modifier) {
    val shape = RoundedCornerShape(size * (24f / 108f))
    val moonPath = remember { PathParser().parsePathString("M35,58.5 A19,19 0 0,1 73,58.5 Z").toPath() }
    Surface(
        modifier = modifier.size(size),
        shape = shape,
        color = Color.Transparent,
        shadowElevation = 2.dp,
        border = BorderStroke(0.75.dp, MoeriseColors.Ink.copy(alpha = 0.08f)),
    ) {
        Canvas(Modifier.fillMaxSize()) {
            drawRect(
                brush = Brush.linearGradient(
                    colors = listOf(Color(0xFFFAF9F5), Color(0xFFECE9E0)),
                    start = Offset.Zero,
                    end = Offset(this.size.width, this.size.height),
                ),
            )
            val scale = minOf(this.size.width, this.size.height) / 108f
            withTransform({ scale(scale, scale, pivot = Offset.Zero) }) {
                drawPath(moonPath, MoeriseColors.Accent)
                drawLine(MoeriseColors.Accent, Offset(27f, 63.5f), Offset(81f, 63.5f), strokeWidth = 3.6f, cap = StrokeCap.Round)
                drawLine(MoeriseColors.Accent.copy(alpha = 0.55f), Offset(35f, 71f), Offset(73f, 71f), strokeWidth = 3.2f, cap = StrokeCap.Round)
                drawLine(MoeriseColors.Accent.copy(alpha = 0.30f), Offset(44f, 78f), Offset(64f, 78f), strokeWidth = 2.8f, cap = StrokeCap.Round)
            }
        }
    }
}
