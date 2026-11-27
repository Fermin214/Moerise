package io.github.fermin214.moerise.presentation

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance
import kotlin.test.Test
import kotlin.test.assertTrue

class MoerisePaletteTest {
    @Test
    fun textAndControlBoundariesMeetWcagContrastAcrossBothPalettes() {
        fun contrast(foreground: Color, background: Color, minimum: Float) {
            val first = foreground.luminance()
            val second = background.luminance()
            val ratio = (maxOf(first, second) + .05f) / (minOf(first, second) + .05f)
            assertTrue(ratio >= minimum, "$foreground on $background: $ratio < $minimum")
        }
        for (palette in listOf(LightMoerisePalette, DarkMoerisePalette)) {
            for (layer in listOf(palette.Paper, palette.Surface, palette.SheetSurface)) {
                for (ink in listOf(palette.Ink, palette.Muted, palette.InkTag, palette.InkColophon,
                    palette.Accent, palette.Danger)) contrast(ink, layer, 4.5f)
                contrast(palette.FieldBorder, layer, 3f)
            }
            contrast(palette.OnAccent, palette.Accent, 4.5f)
            contrast(palette.OnDanger, palette.Danger, 4.5f)
            contrast(palette.Accent, palette.SelectedWash, 4.5f)
            contrast(palette.Accent, palette.HeatmapLight, 4.5f)
            contrast(palette.Accent, palette.HeatmapMedium, 4.5f)
        }
    }
}
