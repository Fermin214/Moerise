package io.github.fermin214.moerise.presentation

import androidx.compose.animation.core.animate
import androidx.compose.animation.core.spring
import androidx.compose.foundation.clickable
import androidx.compose.foundation.focusGroup
import androidx.compose.foundation.focusable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.*
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.runtime.*
import androidx.compose.ui.AbsoluteAlignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.PointerType
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.util.VelocityTracker
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.dismiss
import androidx.compose.ui.semantics.paneTitle
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlin.math.abs
import kotlin.math.roundToInt

internal class NavigationDrawerState(private val scope: CoroutineScope) {
    var fraction by mutableFloatStateOf(0f)
        private set
    var isOpen by mutableStateOf(false)
        private set
    val visible by derivedStateOf { isOpen || fraction > 0f }
    private var animation: Job? = null

    fun open() = settle(true)
    fun close() = settle(false)

    fun startDrag() { animation?.cancel() }

    fun drag(delta: Float, width: Float) {
        fraction = (fraction + delta / width).coerceIn(0f, 1f)
    }

    fun finishDrag(released: Boolean, velocity: Float, threshold: Float) = settle(when {
        !released -> isOpen
        isOpen -> velocity >= -threshold && fraction > .68f
        else -> velocity > threshold || fraction >= .32f
    })

    private fun settle(open: Boolean) {
        animation?.cancel()
        isOpen = open
        animation = scope.launch {
            animate(fraction, if (open) 1f else 0f, animationSpec = spring()) { value, _ ->
                fraction = value.coerceIn(0f, 1f)
            }
        }
    }
}

private enum class DragAxis { Horizontal, Vertical }

private fun dragAxis(movement: Offset, slop: Float): DragAxis? = when {
    abs(movement.y) > slop && abs(movement.y) >= abs(movement.x) -> DragAxis.Vertical
    abs(movement.x) > slop && abs(movement.x) > abs(movement.y) -> DragAxis.Horizontal
    else -> null
}

internal fun canStartDrawerDrag(position: Offset, edge: Float, excluded: Collection<Rect>) =
    position.x >= edge && excluded.none { it.contains(position) }

internal fun takesDrawerDrag(movement: Offset, slop: Float, open: Boolean) =
    dragAxis(movement, slop) == DragAxis.Horizontal && if (open) movement.x < 0f else movement.x > 0f

private val LocalDrawerGestureRegions = staticCompositionLocalOf<MutableMap<Any, Rect>?> { null }
private val LocalDrawerOpener = staticCompositionLocalOf<MutableState<FocusRequester?>?> { null }

@Composable
internal fun Modifier.drawerOpener(): Modifier {
    val opener = LocalDrawerOpener.current ?: return this
    val requester = remember { FocusRequester() }
    DisposableEffect(opener, requester) {
        opener.value = requester
        onDispose { if (opener.value === requester) opener.value = null }
    }
    return focusRequester(requester)
}

@Composable
internal fun Modifier.drawerGestureRegion(): Modifier {
    val regions = LocalDrawerGestureRegions.current ?: return this
    val token = remember { Any() }
    DisposableEffect(regions, token) { onDispose { regions.remove(token) } }
    return onGloballyPositioned { regions[token] = it.boundsInRoot() }
}

@Composable
internal fun NavigationDrawer(
    drawer: NavigationDrawerState, enabled: Boolean, strings: Strings, onDragStart: () -> Unit,
    modifier: Modifier = Modifier, sheet: @Composable ColumnScope.() -> Unit, content: @Composable () -> Unit,
) {
    val regions = remember { mutableMapOf<Any, Rect>() }
    val opener = remember { mutableStateOf<FocusRequester?>(null) }
    val sheetFocus = remember { FocusRequester() }
    var opened by remember { mutableStateOf(false) }
    LaunchedEffect(drawer.isOpen, drawer.visible) {
        if (drawer.isOpen) {
            sheetFocus.requestFocus()
            opened = true
        } else if (!drawer.visible && opened) {
            opened = false
            opener.value?.requestFocus()
        }
    }
    val dragStart by rememberUpdatedState(onDragStart)
    var coordinates by remember { mutableStateOf<LayoutCoordinates?>(null) }
    BoxWithConstraints(modifier.fillMaxSize().clipToBounds().onGloballyPositioned { coordinates = it }) {
        val width = minOf(maxWidth * .78f, 296.dp)
        Box(Modifier.fillMaxSize().pointerInput(enabled, drawer, width) {
            if (!enabled || width <= 0.dp) return@pointerInput
            awaitEachGesture {
                val down = awaitFirstDown(requireUnconsumed = false)
                val position = coordinates?.localToRoot(down.position) ?: down.position
                val open = drawer.visible
                if (down.type != PointerType.Touch ||
                    !canStartDrawerDrag(position, 32.dp.toPx(), if (open) emptyList() else regions.values)) return@awaitEachGesture
                var dragging = false
                var released = false
                val velocity = VelocityTracker().apply { addPosition(down.uptimeMillis, down.position) }
                try {
                    while (true) {
                        val event = awaitPointerEvent()
                        if (event.changes.count { it.pressed } > 1) break
                        val change = event.changes.firstOrNull { it.id == down.id } ?: break
                        velocity.addPosition(change.uptimeMillis, change.position)
                        if (!change.pressed) {
                            released = !change.isConsumed
                            if (dragging) change.consume()
                            break
                        }
                        if (change.isConsumed) break
                        val movement = change.position - down.position
                        if (!dragging) {
                            if (change.uptimeMillis - down.uptimeMillis >= viewConfiguration.longPressTimeoutMillis) break
                            val slop = viewConfiguration.touchSlop
                            if (dragAxis(movement, slop) == null) continue
                            if (!takesDrawerDrag(movement, slop, open)) break
                            dragging = true
                            drawer.startDrag()
                            dragStart()
                            drawer.drag(movement.x - if (open) -slop else slop, width.toPx())
                        } else drawer.drag(change.position.x - change.previousPosition.x, width.toPx())
                        change.consume()
                    }
                } finally {
                    if (dragging) drawer.finishDrag(released, velocity.calculateVelocity().x, 350.dp.toPx())
                }
            }
        }) {
            val contentSemantics = if (drawer.isOpen) Modifier.clearAndSetSemantics {} else Modifier
            Box(contentSemantics.focusProperties { onEnter = { if (drawer.isOpen) cancelFocusChange() } }.focusGroup()) {
                CompositionLocalProvider(LocalDrawerGestureRegions provides regions, LocalDrawerOpener provides opener, content = content)
            }
            if (drawer.visible) {
                val scrim = MoeriseColors.DialogScrim
                Box(Modifier.matchParentSize()
                    .drawBehind { drawRect(scrim.copy(alpha = scrim.alpha * drawer.fraction)) }
                    .focusProperties { canFocus = false }
                    .clickable(interactionSource = null, indication = null, role = Role.Button,
                        onClickLabel = strings.text("关闭导航", "Close navigation"), onClick = drawer::close))
                ModalDrawerSheet(
                    modifier = Modifier.align(AbsoluteAlignment.TopLeft)
                        .absoluteOffset { IntOffset(((drawer.fraction - 1f) * width.roundToPx()).roundToInt(), 0) }
                        .width(width)
                        .onPreviewKeyEvent {
                            if (it.key == Key.Escape && it.type == KeyEventType.KeyDown) { drawer.close(); true } else false
                        }
                        .semantics {
                            paneTitle = strings.text("主导航", "Navigation")
                            dismiss { drawer.close(); true }
                        }
                        .focusRequester(sheetFocus)
                        .focusable(),
                    drawerShape = RectangleShape, drawerContainerColor = MoeriseColors.Paper, content = sheet,
                )
            }
        }
    }
}

@Composable
internal fun Modifier.timelineFocusExit(leaveWriting: () -> Unit): Modifier {
    val regions = LocalDrawerGestureRegions.current
    val leave by rememberUpdatedState(leaveWriting)
    var coordinates by remember { mutableStateOf<LayoutCoordinates?>(null) }
    return onGloballyPositioned { coordinates = it }.pointerInput(regions) {
        awaitEachGesture {
            val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Final)
            val position = coordinates?.localToRoot(down.position) ?: down.position
            if (regions?.values?.any { it.contains(position) } == true) return@awaitEachGesture
            while (true) {
                val event = awaitPointerEvent(PointerEventPass.Final)
                if (event.changes.count { it.pressed } > 1) break
                val change = event.changes.firstOrNull { it.id == down.id } ?: break
                val axis = dragAxis(change.position - down.position, viewConfiguration.touchSlop)
                if (axis == DragAxis.Vertical) leave()
                if (axis != null) break
                if (!change.pressed) {
                    if (!change.isConsumed && change.uptimeMillis - down.uptimeMillis < viewConfiguration.longPressTimeoutMillis) leave()
                    break
                }
            }
        }
    }
}
