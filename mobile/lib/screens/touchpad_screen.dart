import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/commands.dart';
import '../core/remote_link.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';

/// The main touchpad.
///
/// Gesture contract:
///
///   * one finger, Drag Mode off -> move the cursor only
///   * one finger, Drag Mode on  -> hold left button and drag (select/drop)
///   * two fingers                -> scroll
///   * tap                        -> left click
///   * two-finger tap             -> right click (opt-in, OFF by default)
///
/// Pointer bookkeeping lives in the raw [Listener] (not in the scale
/// recogniser), because the recogniser reports neither a complete pointer
/// count nor a complete travel distance.
///
/// FIXES in this version:
///  1. `releaseInput()` is no longer sent on every touch. It sends a button-up
///     for ALL buttons, and on Windows a stray right-button-up makes Chrome
///     open the context menu. It is now only sent when a drag is really held,
///     or when the user presses "Release held buttons".
///  2. `ref.watch` inside `_onUpdate` replaced by `ref.read`.
///  3. A right click now requires two fingers to be present (and dwelled) at
///     the moment the first finger lifts, with no real travel.
///  4. Any travel beyond the threshold cancels the two-finger-tap candidate.
class TouchpadScreen extends ConsumerStatefulWidget {
  const TouchpadScreen({super.key});

  @override
  ConsumerState<TouchpadScreen> createState() => _TouchpadScreenState();
}

/// How far a finger must travel before a gesture counts as a drag/movement.
const double _dragThreshold = 8;

/// A movement below this is discarded as sensor jitter.
const double _moveDeadzone = 0.5;

/// Longest a gesture may last and still count as a tap.
const Duration _tapMaxDuration = Duration(milliseconds: 400);

/// How long a second finger must stay down before it counts as a deliberate
/// two-finger tap (filters phantom pointers from the digitiser).
const Duration _twoFingerDwell = Duration(milliseconds: 60);

class _TouchpadScreenState extends ConsumerState<TouchpadScreen>
    with WidgetsBindingObserver {
  /// Whether the left button is currently held down on the PC.
  bool _dragging = false;

  /// Live pointers for the current gesture, keyed by pointer id.
  final Map<int, Offset> _pointers = {};

  /// Centroid of [_pointers] on the previous move.
  Offset? _lastCentroid;

  /// Total centroid travel since the first pointer went down.
  double _rawTravel = 0;

  /// True once a second finger has been down for [_twoFingerDwell].
  bool _twoFingerDwelled = false;

  /// True if, when the FIRST finger lifted, two dwelled fingers were present.
  /// This is what really identifies a two-finger tap.
  bool _twoFingerTapCandidate = false;

  Timer? _dwellTimer;

  /// Whether the user has opted into drag-and-drop for one-finger gestures.
  bool _dragMode = false;

  /// Whether a two-finger tap may produce a right click (OFF by default).
  bool _allowTwoFingerClick = false;

  /// Highest number of simultaneous fingers in this gesture.
  int _peakPointers = 0;

  /// When the current gesture's first pointer went down.
  DateTime? _rawStart;

  /// Travel as the scale recogniser sees it (scroll / arming Drag Mode).
  double _travel = 0;

  /// Sensitivity multiplier applied to drag distance.
  double _speed = 1.0;

  /// Link captured while mounted (ref is unusable from dispose).
  RemoteLink? _capturedLink;

  void _toggleTwoFingerClick() {
    setState(() => _allowTwoFingerClick = !_allowTwoFingerClick);
  }

  void _toggleDragMode() {
    setState(() => _dragMode = !_dragMode);
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _capturedLink = ref.read(remoteLinkProvider);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // Backgrounding can abandon a gesture silently; release a held drag.
    if (state != AppLifecycleState.resumed) _releaseHeldDrag();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _releaseHeldDrag();
    super.dispose();
  }

  /// Cleans local state and releases the left button ONLY if we hold it.
  ///
  /// Deliberately does not call `releaseInput()` unconditionally: that sends
  /// button-ups for every button, and a spurious right-button-up is enough to
  /// open a context menu on Windows.
  void _releaseHeldDrag() {
    _dwellTimer?.cancel();
    _dwellTimer = null;
    _pointers.clear();
    _endDrag();
  }

  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);

    return Padding(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        children: [
          Expanded(
            child: LayoutBuilder(
              builder: (context, constraints) {
                final size = Size(constraints.maxWidth, constraints.maxHeight);
                return Listener(
                  onPointerDown: _onPointerDown,
                  onPointerMove: _onPointerMove,
                  onPointerUp: _onPointerUp,
                  onPointerCancel: (_) => _onCancel(),
                  child: GestureDetector(
                    behavior: HitTestBehavior.opaque,
                    onScaleStart: (details) => _onStart(details, size),
                    onScaleUpdate: (details) => _onUpdate(details, size),
                    onScaleEnd: (details) => _onEnd(details),
                    child: CustomPaint(
                      painter: _TouchpadPainter(active: _dragging),
                      child: SizedBox.expand(
                        child: Center(
                          child: Text(
                            _dragMode
                                ? 'Drag mode armed\nMoving will select on the PC'
                                : 'Swipe to move\nTap to click\nTwo fingers to scroll',
                            textAlign: TextAlign.center,
                            style: TextStyle(
                              color: AppColors.textSecondary,
                              fontSize: 15,
                              fontWeight: FontWeight.w400,
                              height: 1.6,
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
                );
              },
            ),
          ),
          const SizedBox(height: AppTokens.gap),
          RemoteButton(
            label: _dragMode ? 'Drag mode: ON' : 'Drag mode: OFF',
            icon: _dragMode ? Icons.pan_tool_alt : Icons.pan_tool_outlined,
            expand: true,
            active: _dragMode,
            onPressed: remote == null ? null : _toggleDragMode,
          ),
          const SizedBox(height: AppTokens.gapSmall),
          RemoteButton(
            label: _allowTwoFingerClick
                ? 'Two-finger right click: ON'
                : 'Two-finger right click: OFF',
            icon: _allowTwoFingerClick
                ? Icons.touch_app
                : Icons.touch_app_outlined,
            expand: true,
            active: _allowTwoFingerClick,
            onPressed: remote == null ? null : _toggleTwoFingerClick,
          ),
          const SizedBox(height: AppTokens.gapSmall),
          _SpeedRow(
            speed: _speed,
            onChanged: (v) => setState(() => _speed = v),
          ),
          const SizedBox(height: AppTokens.gapSmall),
          _ClickRow(
            enabled: remote != null,
            onLeft: () => remote?.click('left'),
            onRight: () => remote?.click('right'),
          ),
          const SizedBox(height: AppTokens.gapSmall),
          // Manual cure for a stuck button (the only place releaseInput is
          // sent besides explicit user action).
          RemoteButton(
            label: 'Release held buttons',
            icon: Icons.pan_tool,
            expand: true,
            onPressed: remote == null
                ? null
                : () {
                    _endDrag();
                    remote.releaseInput();
                    ScaffoldMessenger.of(context).showSnackBar(
                      const SnackBar(
                        content: Text('Released any buttons held on the PC.'),
                      ),
                    );
                  },
          ),
        ],
      ),
    );
  }

  // ---------------------------------------------------------------------------
  // Raw pointer handling
  // ---------------------------------------------------------------------------

  void _onPointerDown(PointerDownEvent event) {
    if (_pointers.isEmpty) {
      // New gesture: reset all raw bookkeeping.
      _rawTravel = 0;
      _lastCentroid = null;
      _twoFingerDwelled = false;
      _twoFingerTapCandidate = false;
      _peakPointers = 0;
      _rawStart = DateTime.now();
      _dwellTimer?.cancel();
      _dwellTimer = null;
      // FIX: only release if a stale drag is actually held. Previously
      // releaseInput() was sent on EVERY touch, which sends a right-button-up
      // to Windows and makes Chrome show the context menu.
      if (_dragging) _endDrag();
    }
    _pointers[event.pointer] = event.position;
    if (_pointers.length > _peakPointers) _peakPointers = _pointers.length;
    _lastCentroid = _centroid;
    _armTwoFingerDwell();
  }

  void _onPointerMove(PointerMoveEvent event) {
    if (!_pointers.containsKey(event.pointer)) return;
    _pointers[event.pointer] = event.position;
    final centroid = _centroid;
    final previous = _lastCentroid;
    _lastCentroid = centroid;
    if (previous != null) _rawTravel += (centroid - previous).distance;

    // FIX: real travel means this is not a tap of any kind, so it can never
    // become a right click.
    if (_rawTravel > _dragThreshold) {
      _twoFingerTapCandidate = false;
      _twoFingerDwelled = false;
      _dwellTimer?.cancel();
      _dwellTimer = null;
      return;
    }
    _armTwoFingerDwell();
  }

  void _onPointerUp(PointerUpEvent event) {
    // FIX: sample how many fingers are present at the moment of release,
    // BEFORE removing this pointer. A genuine two-finger tap has two dwelled
    // fingers here and almost no travel.
    final fingersAtRelease = _pointers.length;
    if (fingersAtRelease >= 2 &&
        _twoFingerDwelled &&
        _rawTravel <= _dragThreshold) {
      _twoFingerTapCandidate = true;
    }

    _pointers.remove(event.pointer);
    _armTwoFingerDwell();
    if (_pointers.isNotEmpty) {
      _lastCentroid = _centroid;
      return;
    }
    _lastCentroid = null;
    _classifyAndClick();
  }

  /// Emits the click, if any, that the finished gesture represents.
  void _classifyAndClick() {
    final remote = ref.read(remoteProvider);
    final elapsed = DateTime.now().difference(_rawStart ?? DateTime.now());

    final moved = _rawTravel > _dragThreshold;
    final wasTap = elapsed < _tapMaxDuration && !moved;

    // A drag outranks a click.
    if (_dragging || !wasTap) {
      _resetRawState();
      if (remote == null) return;
      if (_dragging) {
        _endDrag();
        if (mounted) setState(() {});
      }
      return;
    }

    if (remote == null) {
      _resetRawState();
      return;
    }

    if (_allowTwoFingerClick && _twoFingerTapCandidate) {
      remote.click('right');
    } else {
      // Ambiguous gestures always fall through to LEFT, never to right.
      remote.click('left');
    }
    _resetRawState();
    if (mounted) setState(() {});
  }

  /// The mean position of the live pointers.
  Offset get _centroid {
    var sum = Offset.zero;
    for (final position in _pointers.values) {
      sum += position;
    }
    return _pointers.isEmpty ? Offset.zero : sum / _pointers.length.toDouble();
  }

  /// Promotes a sustained two-finger contact to [_twoFingerDwelled].
  void _armTwoFingerDwell() {
    if (_pointers.length < 2) {
      if (_dwellTimer != null) {
        _dwellTimer!.cancel();
        _dwellTimer = null;
      }
      return;
    }
    if (_twoFingerDwelled || _dwellTimer != null) return;
    _dwellTimer = Timer(_twoFingerDwell, () {
      _dwellTimer = null;
      if (!mounted) return;
      // Only valid if two fingers are still down when the timer matures.
      if (_pointers.length >= 2) _twoFingerDwelled = true;
    });
  }

  // ---------------------------------------------------------------------------
  // Scale recogniser callbacks (cursor movement and scroll)
  // ---------------------------------------------------------------------------

  void _onStart(ScaleStartDetails details, Size size) {
    _dragging = false;
    _travel = 0;
    setState(() {});
  }

  void _onUpdate(ScaleUpdateDetails details, Size size) {
    // FIX: ref.read, not ref.watch, inside a callback.
    final remote = ref.read(remoteProvider);
    if (remote == null) return;

    final delta = details.focalPointDelta;

    // Two fingers scroll.
    if (details.pointerCount >= 2) {
      final notches = (delta.dy / 40).round();
      final horizontal = (delta.dx / 40).round();
      if (notches != 0 || horizontal != 0) {
        remote.scroll(horizontal, -notches);
      }
      _travel += delta.distance;
      return;
    }

    if (delta.distance < _moveDeadzone) return; // ignore jitter

    _travel += delta.distance;

    // A drag only begins in Drag Mode and once the finger clearly travelled.
    if (_dragMode && !_dragging && _travel > _dragThreshold) {
      _dragging = true;
      remote.buttonDown('left');
    }

    final dx = (delta.dx * _speed).round();
    final dy = (delta.dy * _speed).round();
    if (dx != 0 || dy != 0) remote.moveCursor(dx, dy);
  }

  /// Releases the left button if a drag is still held.
  void _endDrag() {
    if (!_dragging) return;
    // Uses the captured link rather than ref, because this can run from dispose.
    final link = _capturedLink;
    if (link != null) Remote(link).buttonUp('left');
    _dragging = false;
  }

  void _onEnd(ScaleEndDetails details) {
    if (!_dragging) return;
    final remote = ref.read(remoteProvider);
    if (remote != null) remote.buttonUp('left');
    _dragging = false;
    if (mounted) setState(() {});
  }

  /// Called when the gesture is interrupted before completing.
  void _onCancel() {
    _resetRawState();
    _endDrag();
    if (mounted) setState(() {});
  }

  /// Clears the per-gesture raw state.
  void _resetRawState() {
    _dwellTimer?.cancel();
    _dwellTimer = null;
    _pointers.clear();
    _twoFingerDwelled = false;
    _twoFingerTapCandidate = false;
    _rawTravel = 0;
    _lastCentroid = null;
    _peakPointers = 0;
    _rawStart = null;
  }
}

/// Draws the circular touchpad with a cyan neon ring.
class _TouchpadPainter extends CustomPainter {
  _TouchpadPainter({required this.active});

  /// Brightens the ring while a drag is in progress.
  final bool active;

  @override
  void paint(Canvas canvas, Size size) {
    final center = Offset(size.width / 2, size.height / 2);
    final radius = (size.shortestSide / 2) - 8;
    if (radius <= 0) return;

    final rect = Rect.fromCircle(center: center, radius: radius);

    // Glass fill.
    canvas.drawCircle(
      center,
      radius,
      Paint()
        ..shader = RadialGradient(
          colors: [
            AppColors.accent.withValues(alpha: active ? 0.14 : 0.07),
            AppColors.glass,
          ],
        ).createShader(rect),
    );

    // Neon glow.
    canvas.drawCircle(
      center,
      radius,
      Paint()
        ..color = AppColors.accent.withValues(alpha: active ? 0.9 : 0.5)
        ..style = PaintingStyle.stroke
        ..strokeWidth = active ? 3 : 2
        ..maskFilter = const MaskFilter.blur(BlurStyle.normal, 12),
    );
    // Crisp ring.
    canvas.drawCircle(
      center,
      radius,
      Paint()
        ..color = AppColors.accent.withValues(alpha: active ? 1 : 0.7)
        ..style = PaintingStyle.stroke
        ..strokeWidth = active ? 2 : 1,
    );

    // Centre crosshair.
    final tick = Paint()
      ..color = AppColors.accent.withValues(alpha: 0.25)
      ..strokeWidth = 1;
    canvas.drawLine(
      Offset(center.dx - 8, center.dy),
      Offset(center.dx + 8, center.dy),
      tick,
    );
    canvas.drawLine(
      Offset(center.dx, center.dy - 8),
      Offset(center.dx, center.dy + 8),
      tick,
    );
  }

  @override
  bool shouldRepaint(_TouchpadPainter oldDelegate) =>
      oldDelegate.active != active;
}

/// The cursor-speed slider.
class _SpeedRow extends StatelessWidget {
  const _SpeedRow({required this.speed, required this.onChanged});

  final double speed;
  final ValueChanged<double> onChanged;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      padding: const EdgeInsets.symmetric(
        horizontal: AppTokens.gap,
        vertical: 10,
      ),
      child: Row(
        children: [
          const Icon(Icons.speed, size: 18, color: AppColors.accent),
          const SizedBox(width: AppTokens.gapSmall),
          Text('Speed', style: interStyle(13, FontWeight.w600)),
          Expanded(
            child: SliderTheme(
              data: SliderThemeData(
                activeTrackColor: AppColors.accent,
                inactiveTrackColor: AppColors.glassBorder,
                thumbColor: AppColors.accent,
                overlayColor: AppColors.accent.withValues(alpha: 0.15),
                trackHeight: 3,
              ),
              child: Slider(
                value: speed.clamp(0.3, 3.0),
                min: 0.3,
                max: 3.0,
                onChanged: onChanged,
              ),
            ),
          ),
          SizedBox(
            width: 44,
            child: Text(
              '${speed.toStringAsFixed(1)}×',
              textAlign: TextAlign.right,
              style: monoStyle(12, FontWeight.w700, color: AppColors.accent),
            ),
          ),
        ],
      ),
    );
  }
}

/// Explicit left and right click buttons.
class _ClickRow extends StatelessWidget {
  const _ClickRow({
    required this.enabled,
    required this.onLeft,
    required this.onRight,
  });

  final bool enabled;
  final VoidCallback onLeft;
  final VoidCallback onRight;

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        Expanded(
          child: RemoteButton(
            label: 'Left click',
            icon: Icons.ads_click,
            expand: true,
            onPressed: enabled ? onLeft : null,
          ),
        ),
        const SizedBox(width: AppTokens.gapSmall),
        Expanded(
          child: RemoteButton(
            label: 'Right click',
            icon: Icons.ads_click,
            expand: true,
            onPressed: enabled ? onRight : null,
          ),
        ),
      ],
    );
  }
}
