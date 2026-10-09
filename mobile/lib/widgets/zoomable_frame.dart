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
    _controller.addListener(() => setState(() {}));
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
