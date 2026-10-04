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
///   * tap                        -> click
///
/// Moving the cursor must never hold a button down. Holding left while
/// travelling is exactly what produces a text selection on the PC, so a
/// one-finger swipe turned every gesture into a drag-and-select. Because that
/// is the most frequent gesture by far, the default is move-only and
/// drag-and-drop is opt-in, which keeps the common case free of side effects
/// while still allowing the behaviour when it is wanted.
class TouchpadScreen extends ConsumerStatefulWidget {
  const TouchpadScreen({super.key});

  @override
  ConsumerState<TouchpadScreen> createState() => _TouchpadScreenState();
}

/// How far a finger must travel before a gesture counts as a drag rather
/// than movement or a tap.
///
/// Without a threshold, the natural tremor of a resting finger is enough to
/// start a drag, so a stationary thumb would click-and-drag on the PC.
const double _dragThreshold = 8;

/// A movement below this is discarded as sensor jitter.
const double _moveDeadzone = 0.5;

class _TouchpadScreenState extends ConsumerState<TouchpadScreen>
    with WidgetsBindingObserver {
  /// The most pointers seen during the current gesture.
  ///
  /// This is what distinguishes a one-finger tap (left click) from a
  /// two-finger tap (right click). The scale recogniser reports its start as
  /// soon as the *first* finger lands, so a snapshot taken at gesture start is
  /// 1 even for a two-finger gesture: the second finger arrives milliseconds
  /// later, after the start has already fired. Counting the peak is what lets a
  /// two-finger tap be recognised as a right click at all.
  int _maxPointers = 0;

  /// Whether the left button is currently held down on the PC.
  ///
  /// This is the only thing that turns movement into a selection, so it is
  /// released unconditionally on gesture end and on cancel.
  bool _dragging = false;

  /// Whether the user has opted into drag-and-drop for one-finger gestures.
  bool _dragMode = false;

  /// Total movement since the gesture started, in logical pixels.
  double _travel = 0;

  /// When the gesture started, used to classify a tap versus a drag.
  DateTime? _startTime;

  /// Sensitivity multiplier applied to drag distance.
  double _speed = 1.0;

  /// The link captured while this widget is mounted.
  ///
  /// Needed because `ref` is unusable from `dispose`, yet the release on the
  /// way out is the single most important thing this screen does.
  RemoteLink? _capturedLink;

  /// Turns drag-and-drop on or off.
  ///
  /// Turning it off mid-gesture is not possible, but leaving Drag Mode
  /// enabled while the previous drag is still held would strand the button,
  /// so the mode is reset whenever a gesture ends.
  void _toggleDragMode() {
    setState(() => _dragMode = !_dragMode);
  }

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    // Captured now, while ref is valid, so the dispose-time release works.
    _capturedLink = ref.read(remoteLinkProvider);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // Backgrounding the app does NOT reliably fire onScaleCancel or
    // onScaleEnd: a call arriving mid-drag, a notification shade pulled down,
    // or a plain switch to another app all abandon the gesture silently. When
    // that happens the PC's left button is left held down, and from then on
    // every cursor movement selects text. There is no visible cause and no
    // visible cure, which makes this by far the most damaging bug this screen
    // can have - so it is handled explicitly rather than hoped away.
    if (state != AppLifecycleState.resumed) _releaseEverything();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    // Leaving the screen mid-drag would otherwise leave the PC's left button
    // held down, which is the worst failure mode this screen can have: the
    // user would click-and-drag on their own machine with no way to see why.
    _releaseEverything();
    super.dispose();
  }

  /// Releases the drag and tells the PC to drop anything it still holds.
  ///
  /// Called from both dispose and the lifecycle observer. Riverpod forbids
  /// touching `ref` once the element is disposed, and by dispose the ref is
  /// already dead, so the link is read from a value captured while mounted.
  ///
  /// That captured link stays valid regardless: it belongs to the pairing
  /// rather than to this screen, so it outlives the widget.
  void _releaseEverything() {
    _endDrag();
    final link = _capturedLink;
    if (link == null) return;
    Remote(link).releaseInput();
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
                  // A Listener sits outside the GestureDetector purely to catch
                  // gesture abandonment. GestureDetector no longer exposes
                  // onScaleCancel, so an interrupted gesture (an incoming call, a
                  // system edge-swipe, a notification shade) would otherwise
                  // never release a held button. onPointerCancel fires in
                  // exactly those cases, and because it is a raw pointer
                  // callback it does not compete in the gesture arena.
                  onPointerCancel: (_) => _onCancel(),
                  child: GestureDetector(
                      behavior: HitTestBehavior.opaque,
                      // The pointer count is sampled on the first pointer down
                      // and again on scale start, which is how two-finger
                      // gestures are distinguished.
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
          // Drag Mode is opt-in and visibly armed, so a drag is never a
          // surprise and the user always knows why the cursor is selecting.
          RemoteButton(
            label: _dragMode ? 'Drag mode: ON' : 'Drag mode: OFF',
            icon: _dragMode ? Icons.pan_tool_alt : Icons.pan_tool_outlined,
            expand: true,
            active: _dragMode,
            onPressed: remote == null ? null : _toggleDragMode,
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
          // A visible cure for a stuck button. The automatic releases above
          // cover every case this app can observe, but if the PC is already in
          // the stuck state before the app starts, the user needs a way out
          // that does not depend on the app correctly diagnosing what happened.
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

  void _onStart(ScaleStartDetails details, Size size) {
    // Clear any button left held by an earlier gesture before this one begins.
    // A stale hold is invisible locally - _dragging was already reset when that
    // widget rebuilt - but on the PC it turns every subsequent movement into a
    // text selection. One cheap message here is what makes the pad reliably
    // move-only, regardless of how the previous gesture ended.
    ref.read(remoteProvider)?.releaseInput();

    _maxPointers = details.pointerCount;
    _dragging = false;
    _travel = 0;
    _startTime = DateTime.now();
    setState(() {});
  }

  void _onUpdate(ScaleUpdateDetails details, Size size) {
    final remote = ref.watch(remoteProvider);
    if (remote == null) return;

    final delta = details.focalPointDelta;
    if (details.pointerCount > _maxPointers) {
      _maxPointers = details.pointerCount;
    }

    // Two fingers scroll, and take priority over everything else: a second
    // finger often lands a few milliseconds after the first, so the branch is
    // keyed on the live pointer count rather than the count at gesture start.
    if (details.pointerCount >= 2) {
      // One wheel notch per 40 logical pixels of travel.
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

    // A drag only begins in Drag Mode and only once the finger has clearly
    // travelled. This is what keeps a plain swipe from selecting text.
    if (_dragMode && !_dragging && _travel > _dragThreshold) {
      _dragging = true;
      remote.buttonDown('left');
    }

    final dx = (delta.dx * _speed).round();
    final dy = (delta.dy * _speed).round();
    if (dx != 0 || dy != 0) remote.moveCursor(dx, dy);
  }

  /// Releases the button if a drag is still held.
  ///
  /// Called from both end and cancel: a gesture interrupted by a phone call or
  /// a system gesture would otherwise leave the PC's left button stuck down.
  void _endDrag() {
    if (!_dragging) return;
    // Uses the captured link rather than ref, because this runs from dispose.
    final link = _capturedLink;
    if (link != null) Remote(link).buttonUp('left');
    _dragging = false;
  }

  void _onEnd(ScaleEndDetails details) {
    final remote = ref.watch(remoteProvider);
    if (remote == null) return;

    if (_dragging) {
      // Drag Mode stays on for the next gesture so a long drag does not need
      // re-arming halfway through, but the button is always released here.
      remote.buttonUp('left');
      _dragging = false;
      setState(() {});
      return;
    }

    // No drag was engaged: classify by peak pointer count and duration.
    // Movement alone is never a click, so a long swipe cannot click the PC.
    //
    // The peak pointer count is used rather than the count at gesture start,
    // because the recogniser reports its start on the first finger down - see
    // _maxPointers. Using the start count would classify every two-finger tap as
    // a left click, so a right click would be impossible to perform by gesture.
    final elapsed = DateTime.now().difference(_startTime ?? DateTime.now());
    final wasTap = elapsed < const Duration(milliseconds: 400) &&
        _travel < _dragThreshold;

    if (wasTap && _maxPointers >= 2) {
      // Two fingers down without travel is a right click.
      remote.click('right');
    } else if (wasTap) {
      // A single finger taps for a left click.
      remote.click('left');
    }
    setState(() {});
  }

  /// Called when the gesture is interrupted before completing.
  void _onCancel() {
    _endDrag();
    setState(() {});
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

    // The neon ring is the primary state cue, so it is drawn with a blur for
    // the glow and a crisp stroke on top so it stays visible on any screen.
    canvas.drawCircle(
      center,
      radius,
      Paint()
        ..color = AppColors.accent.withValues(alpha: active ? 0.9 : 0.5)
        ..style = PaintingStyle.stroke
        ..strokeWidth = active ? 3 : 2
        ..maskFilter = const MaskFilter.blur(BlurStyle.normal, 12),
    );
    canvas.drawCircle(
      center,
      radius,
      Paint()
        ..color = AppColors.accent.withValues(alpha: active ? 1 : 0.7)
        ..style = PaintingStyle.stroke
        ..strokeWidth = active ? 2 : 1,
    );

    // Centre crosshair, giving the pad a visible centre reference.
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
          horizontal: AppTokens.gap, vertical: 10),
      child: Row(
        children: [
          const Icon(Icons.speed, size: 18, color: AppColors.accent),
          const SizedBox(width: AppTokens.gapSmall),
          // A text label accompanies the icon so the control is identifiable
          // without relying on the glyph.
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
///
/// These are always available so a right click never depends on the gesture
/// recogniser succeeding, which is unreliable on some Android keyboards and
/// trackpads.
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
