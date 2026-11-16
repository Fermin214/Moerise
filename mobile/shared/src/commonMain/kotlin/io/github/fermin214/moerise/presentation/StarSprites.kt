package io.github.fermin214.moerise.presentation

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.*
import androidx.compose.ui.graphics.drawscope.CanvasDrawScope
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection

private val specialCore = Color(0xFFFBF5E8)
private const val tileSize = 128

/** 同一张纹理容纳星核和光晕，避免逐星切换渐变着色器。 */
internal class StarSprites(colors: List<Color>) {
    private val palette = (colors + specialCore).distinct()
    private val rows = palette.withIndex().associate { it.value to it.index * tileSize }
    private val bitmap = ImageBitmap(tileSize * 2, tileSize * palette.size)
    private val paint = Paint().apply { filterQuality = FilterQuality.Low }
    private val sourceSize = IntSize(tileSize, tileSize)

    init {
        CanvasDrawScope().draw(Density(1f), LayoutDirection.Ltr, Canvas(bitmap),
            Size(bitmap.width.toFloat(), bitmap.height.toFloat())) {
            palette.forEachIndexed { index, color ->
                val center = Offset(tileSize / 2f, index * tileSize + tileSize / 2f)
                drawCircle(Brush.radialGradient(
                    listOf(color.copy(alpha = .20f), color.copy(alpha = .035f), Color.Transparent),
                    center = center, radius = tileSize * 7f / 16f),
                    radius = tileSize * 7f / 16f, center = center)
                drawCircle(color, radius = tileSize / 4f, center = center + Offset(tileSize.toFloat(), 0f))
            }
        }
    }

    fun draw(canvas: Canvas, position: Offset, radius: Float, color: Color, alpha: Float,
        haloAlpha: Float, special: Boolean, photo: Boolean) {
        canvas.save()
        try {
            // 用浮点变换保留手势中的亚像素位移，星核和光晕分别应用原有透明度。
            canvas.translate(position.x, position.y)
            canvas.scale(radius, radius)
            paint.alpha = alpha * haloAlpha
            canvas.drawImageRect(bitmap, IntOffset(0, rows.getValue(color)), sourceSize,
                IntOffset(-8, -8), IntSize(16, 16), paint)
            val coreScale = if (photo) .76f else .8f
            canvas.scale(coreScale, coreScale)
            paint.alpha = alpha
            canvas.drawImageRect(bitmap, IntOffset(tileSize, rows.getValue(if (special) specialCore else color)), sourceSize,
                IntOffset(-2, -2), IntSize(4, 4), paint)
        } finally {
            canvas.restore()
        }
    }
}
