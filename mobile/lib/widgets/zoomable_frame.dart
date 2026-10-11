import 'dart:ui' as ui;

import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// Pinch-to-zoom, pan and double-tap viewer for a decoded MJPEG frame.
///
/// The transform lives in [_controller] (owned by this State) so it survives
/// per-frame rebuilds. Do NOT give this widget a changing key (e.g.
/// a sequence-based ValueKey): that would recreate the State and reset the
/// zoom on every frame.
///
/// The outer [Stack] uses [StackFit.expand] so the [InteractiveViewer] fills
/// the whole panel instead of shrinking to the picture size (a bare Stack
/// hands loose constraints to its children and would prevent the expansion).
/// The [Center] sits *inside* the viewer so the 1x layout stays pixel-equal
/// to the previous plain AspectRatio view.
///
/// Layout: every frame is fitted to the available space with its own aspect
/// ratio preserved ([Center] + [AspectRatio] + `BoxFit.contain`), so a
/// 1680x1050 desktop is drawn larger than a 1920x1080 one rather than being
/// pinned to the width the first frame happened to use. The zoom transform is
/// kept across frames of the same ratio, and reset when the ratio changes -
/// see [didUpdateWidget].
class ZoomableFrame extends StatefulWidget {
  const ZoomableFrame({super.key, required this.frame, this.maxScale = 6});

  final ui.Image frame;
  final double maxScale;

  @override
  State<ZoomableFrame> createState() => _ZoomableFrameState();
}

class _ZoomableFrameState extends State<ZoomableFrame>
    with SingleTickerProviderStateMixin {
  final TransformationController _controller = TransformationController();
  late final AnimationController _anim;
  Animation<Matrix4>? _tween;
  Offset _tapAt = Offset.zero;

  // Set while the controller is written to programmatically from
  // didUpdateWidget. That write fires the listener, and the listener calls
  // setState, which Flutter rejects while the frame is still building.
  bool _suppressRebuild = false;

  bool get _zoomed => _controller.value.getMaxScaleOnAxis() > 1.01;

  @override
  void initState() {
    super.initState();
    _anim = AnimationController(
        vsync: this, duration: const Duration(milliseconds: 220))
      ..addListener(() {
        final t = _tween;
        if (t != null) _controller.value = t.value;
      });
    _controller.addListener(() {
      if (!_suppressRebuild) setState(() {});
    });
  }

  // A frame whose aspect ratio differs from the previous one is a different
  // shape, not merely a new picture of the same one. Keeping the old transform
  // across that change leaves the picture offset and cropped by whatever the
  // previous zoom and pan happened to be, and the user has no gesture that
  // restores it because the pill's "reset" is the only thing that can.
  //
  // Only the ratio matters here. Every frame arrives at a new size - the same
  // desktop simply produces another JPEG - so resetting on size alone would
  // throw away the user's zoom fifteen times a second.
  @override
  void didUpdateWidget(covariant ZoomableFrame oldWidget) {
    super.didUpdateWidget(oldWidget);
    final oldFrame = oldWidget.frame;
    final newFrame = widget.frame;
    if (oldFrame.width <= 0 || oldFrame.height <= 0) return;
    if (newFrame.width <= 0 || newFrame.height <= 0) return;

    final oldAspect = oldFrame.width / oldFrame.height;
    final newAspect = newFrame.width / newFrame.height;
    if ((oldAspect - newAspect).abs() < 0.0001) return;
    if (!_zoomed) return;

    _anim.stop();
    _tween = null;
    _suppressRebuild = true;
    _controller.value = Matrix4.identity();
    _suppressRebuild = false;
  }

  @override
  void dispose() {
    _anim.dispose();
    _controller.dispose();
    super.dispose();
  }

  void _animateTo(Matrix4 end) {
    _tween = Matrix4Tween(begin: _controller.value, end: end).animate(
        CurvedAnimation(parent: _anim, curve: Curves.easeOutCubic));
    _anim.forward(from: 0);
  }

  void _reset() => _animateTo(Matrix4.identity());

  void _onDoubleTap() {
    if (_zoomed) return _reset();
    const s = 2.5;
    // _tapAt is captured via onDoubleTapDown in the GestureDetector wrapping
    // the viewer, so it is already expressed in the viewer's local space.
    final p = _tapAt;
    final m = Matrix4.diagonal3Values(s, s, 1);
    m.setTranslationRaw(-p.dx * (s - 1), -p.dy * (s - 1), 0);
    _animateTo(m);
  }

  @override
  Widget build(BuildContext context) {
    final f = widget.frame;
    // NOTE: widget.frame is read on every build and never cached in State, so
    // disposing the previous ui.Image in the parent setState can never leave
    // this RawImage pointing at a disposed image.
    return Stack(
      fit: StackFit.expand,
      children: [
        GestureDetector(
          behavior: HitTestBehavior.opaque,
          onDoubleTapDown: (d) => _tapAt = d.localPosition,
          onDoubleTap: _onDoubleTap,
          child: InteractiveViewer(
            transformationController: _controller,
            minScale: 1,
            maxScale: widget.maxScale,
            clipBehavior: Clip.hardEdge,
            child: Center(
              child: AspectRatio(
                aspectRatio: f.width / f.height,
                child: RawImage(
                  image: f,
                  fit: BoxFit.contain,
                  filterQuality:
                      _zoomed ? FilterQuality.high : FilterQuality.medium,
                ),
              ),
            ),
          ),
        ),
        if (_zoomed)
          Positioned(
            top: 6,
            right: 6,
            child: Material(
              color: AppColors.background.withValues(alpha: 0.85),
              shape: StadiumBorder(
                  side: BorderSide(
                      color: AppColors.accent.withValues(alpha: 0.65),
                      width: 1)),
              child: InkWell(
                customBorder: const StadiumBorder(),
                onTap: _reset,
                child: Padding(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
                  child: Text(
                      '${_controller.value.getMaxScaleOnAxis().toStringAsFixed(1)}x  ✕',
                      style: const TextStyle(
                          color: AppColors.textPrimary,
                          fontSize: 12,
                          fontWeight: FontWeight.w700)),
                ),
              ),
            ),
          ),
      ],
    );
  }
}
