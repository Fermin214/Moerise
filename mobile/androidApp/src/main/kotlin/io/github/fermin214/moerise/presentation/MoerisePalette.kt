package io.github.fermin214.moerise.presentation

import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight

@Immutable
data class MoerisePalette(
    val dark: Boolean,
    val Paper: Color,
    val Surface: Color,
    val SheetSurface: Color,
    val TabTrack: Color,
    val SurfaceMuted: Color,
    val Hover: Color,
    val SelectedWash: Color,
    val Line: Color,
    val CardBorder: Color,
    val FieldBorder: Color,
    val Ink: Color,
    val BrandInk: Color,
    val Muted: Color,
    val InkTag: Color,
    val InkColophon: Color,
    val Accent: Color,
    val AccentHover: Color,
    val OnAccent: Color,
    val AccentSurface: Color,
    val AccentSurfaceStrong: Color,
    val HeatmapLight: Color,
    val HeatmapMedium: Color,
    val Danger: Color,
    val OnDanger: Color,
    val DangerSurface: Color,
    val Scrim: Color,
    val DialogScrim: Color,
    val ImageBorder: Color,
    val BrandGradient: List<Color>,
    val BrandBorder: Color,
) {
    val ScrollPaper get() = Paper
    val HeadingWeight get() = if (dark) FontWeight.Medium else FontWeight.SemiBold
    val OnImageOverlay get() = Color(0xFFF7F6F2)
}

val LightMoerisePalette = MoerisePalette(
    dark = false,
    Paper = Color(0xFFF7F6F2),
    Surface = Color(0xFFFCFBF8),
    SheetSurface = Color(0xFFFCFBF8),
    TabTrack = Color(0xFFEFECE5),
    SurfaceMuted = Color(0xFFECEAE4),
    Hover = Color(0xFFE6E3DC),
    SelectedWash = Color(0xFFECF1F0),
    Line = Color(0xFFE4E1D8),
    CardBorder = Color(0xFFE4E1D8),
    FieldBorder = Color(0xFF918B80),
    Ink = Color(0xFF1F1E1C),
    BrandInk = Color(0xFF2B2926),
    Muted = Color(0xFF68645E),
    InkTag = Color(0xFF64605A),
    InkColophon = Color(0xFF757069),
    Accent = Color(0xFF2F5D62),
    AccentHover = Color(0xFF254B4F),
    OnAccent = Color(0xFFFFFFFF),
    AccentSurface = Color(0xFFE8F0EE),
    AccentSurfaceStrong = Color(0xFFDCE8E5),
    HeatmapLight = Color(0xFFE3E7E3),
    HeatmapMedium = Color(0xFFCCD7D3),
    Danger = Color(0xFFB42318),
    OnDanger = Color(0xFFFFFFFF),
    DangerSurface = Color(0xFFFDF2F1),
    Scrim = Color(0xFF1F1E1C).copy(alpha = .18f),
    DialogScrim = Color(0xFF1F1E1C).copy(alpha = .32f),
    ImageBorder = Color(0xFF1F1E1C).copy(alpha = .08f),
    BrandGradient = listOf(Color(0xFFFAF9F5), Color(0xFFECE9E0)),
    BrandBorder = Color(0xFF1F1E1C).copy(alpha = .08f),
)

val DarkMoerisePalette = MoerisePalette(
    dark = true,
    Paper = Color(0xFF161614),
    Surface = Color(0xFF1F1E1B),
    SheetSurface = Color(0xFF262521),
    TabTrack = Color(0xFF1B1A17),
    SurfaceMuted = Color(0xFF262521),
    Hover = Color(0xFF2B2A26),
    SelectedWash = Color(0xFF202E2F),
    Line = Color(0xFF2A2925),
    CardBorder = Color(0xFFEBE6DA).copy(alpha = .08f),
    FieldBorder = Color(0xFF757168),
    Ink = Color(0xFFE3E0D8),
    BrandInk = Color(0xFFE3E0D8),
    Muted = Color(0xFFA6A299),
    InkTag = Color(0xFFA6A299),
    InkColophon = Color(0xFF928E85),
    Accent = Color(0xFF73ACAE),
    AccentHover = Color(0xFF84B9BB),
    OnAccent = Color(0xFF141918),
    AccentSurface = Color(0xFF202E2F),
    AccentSurfaceStrong = Color(0xFF263A3C),
    HeatmapLight = Color(0xFF1E2C2D),
    HeatmapMedium = Color(0xFF263A3C),
    Danger = Color(0xFFD96B5C),
    OnDanger = Color(0xFF141918),
    DangerSurface = Color(0xFF2D1D1B),
    Scrim = Color(0xFF0A0A09).copy(alpha = .52f),
    DialogScrim = Color(0xFF0A0A09).copy(alpha = .52f),
    ImageBorder = Color(0xFFEBE6DA).copy(alpha = .08f),
    BrandGradient = listOf(Color(0xFF262521), Color(0xFF1C1B18)),
    BrandBorder = Color(0xFFEBE6DA).copy(alpha = .10f),
)

val LocalMoeriseColors = compositionLocalOf { LightMoerisePalette }
val MoeriseColors: MoerisePalette
    @Composable @ReadOnlyComposable get() = LocalMoeriseColors.current
