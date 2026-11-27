package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
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
import androidx.compose.ui.unit.dp

enum class WebIcon {
    Menu,
    Search,
    Plus,
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
 * 使用 Web 的 24 × 24 坐标、1.8 描边和圆角端点；空描述不添加图像语义。
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
    val stroke = remember(icon) { Stroke(width = if (icon == WebIcon.More) 2.8f else 1.8f, cap = StrokeCap.Round, join = StrokeJoin.Round) }
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
                    tint, radius = 6f, center = Offset(11f, 11f), style = stroke,
                )
                WebIcon.Review, WebIcon.Archive -> drawRoundRect(
                    tint,
                    topLeft = Offset(4f, 5f),
                    size = Size(16f, 15f),
                    cornerRadius = CornerRadius(3f, 3f),
                    style = stroke,
                )
                WebIcon.Map -> {
                    drawCircle(tint, radius = 2.5f, center = Offset(6f, 7f), style = stroke)
                    drawCircle(tint, radius = 2.5f, center = Offset(18f, 6f), style = stroke)
                    drawCircle(tint, radius = 2.5f, center = Offset(12f, 18f), style = stroke)
                }
                WebIcon.Settings -> drawCircle(
                    tint, radius = 3f, center = Offset(12f, 12f), style = stroke,
                )
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
    WebIcon.Menu -> "M4 6h16M4 12h16M4 18h16"
    WebIcon.Search -> "M20 20l-4.5-4.5"
    WebIcon.Plus -> "M12 5v14M5 12h14"
    WebIcon.Close -> "m6 6 12 12M6 18 18 6"
    WebIcon.Home -> "M5 6h14M5 12h14M5 18h9"
    WebIcon.Review -> "M8 3v4M16 3v4M4 10h16M9 15l2 2 4-4"
    WebIcon.Walk -> "M4 8h3c4 0 6 8 10 8h3M4 16h3c1.6 0 2.8-1.2 3.8-2.6" +
        "M20 8h-3c-1.4 0-2.5.9-3.4 2M18 5l2 3-2 3M18 13l2 3-2 3"
    WebIcon.Map -> "M8.3 8.3l2.4 7.4M15.9 8l-2.8 7.8M8.5 7h7"
    WebIcon.Settings -> "M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1" +
        "M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1"
    WebIcon.User -> "M5 20c1-4 4-5.5 7-5.5s6 1.5 7 5.5"
    WebIcon.More -> "M5 12h.01M12 12h.01M19 12h.01"
    WebIcon.ChevronLeft -> "M14 6l-6 6 6 6"
    WebIcon.ChevronRight -> "M10 6l6 6-6 6"
    WebIcon.ArrowDown -> "M12 5v14M6 13l6 6 6-6"
    // 归档使用 Web 日历的容器外框与横向分隔线。
    WebIcon.Archive -> "M4 10h16"
    WebIcon.Refresh -> "M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1"
    WebIcon.Sync -> "M7.5 18h9.2a4.3 4.3 0 0 0 .5-8.57A5.5 5.5 0 0 0 6.5 8.1 3.8 3.8 0 0 0 7.5 18Z"
    WebIcon.Check -> "M5 12l4 4L19 6"
    WebIcon.CloudOff -> "M3 3l18 18M7 7a6 6 0 0 1 10.3 3.2A4 4 0 0 1 20 17M5 10a4 4 0 0 0 1 8h10"
    WebIcon.Alert -> "M12 8v5M12 17h.01M10.3 3.8L2.8 18a2 2 0 0 0 1.7 3h15a2 2 0 0 0 1.7-3L13.7 3.8a2 2 0 0 0-3.4 0"
    WebIcon.SignIn -> "M14 4h6v16h-6M3 12h12M10 7l5 5-5 5"
    WebIcon.Eye -> "M2 12s3.6-6 10-6 10 6 10 6-3.6 6-10 6S2 12 2 12Z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z"
    WebIcon.EyeOff -> "M3 3l18 18M10.6 6.1c6.9-.8 11.4 5.9 11.4 5.9a17 17 0 0 1-3 3.3M14 17.8C6.4 19.3 2 12 2 12a18 18 0 0 1 4.1-4.1"
}
