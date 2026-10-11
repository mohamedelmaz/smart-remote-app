import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smart_remote/widgets/zoomable_frame.dart';

/// Mints a real [ui.Image] of an exact size without network access or an
/// image codec: the recorded picture is the source of truth for its
/// dimensions, so a layout bug shows up here as a wrong drawn size.
Future<ui.Image> _image(int w, int h) {
  final recorder = ui.PictureRecorder();
  final canvas = Canvas(recorder);
  canvas.drawRect(
    Rect.fromLTWH(0, 0, w.toDouble(), h.toDouble()),
    Paint()..color = const Color(0xFF3366FF),
  );
  final picture = recorder.endRecording();
  return picture.toImage(w, h).then((i) {
    picture.dispose();
    return i;
  });
}

/// Mints a frame inside the test's async zone and hands back a value the
/// tests can pass straight into non-nullable parameters.
Future<ui.Image> _mk(WidgetTester tester, int w, int h) async =>
    (await tester.runAsync(() => _image(w, h))) as ui.Image;

/// The panel the viewer is given in production: whatever the phone leaves
/// over after the header and the controls. Re-pumping keeps the same
/// [ZoomableFrame] State alive, exactly as per-frame rebuilds do.
Future<void> _pump(
    WidgetTester tester, ui.Image frame, {Size panel = const Size(384, 500)}) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      body: Center(
        child: SizedBox(
          width: panel.width,
          height: panel.height,
          child: ZoomableFrame(frame: frame),
        ),
      ),
    ),
  ));
  await tester.pumpAndSettle();
}

/// The contain-fit rectangle for [frame] inside [panel]: the largest box with
/// the frame's own ratio that still fits, centred.
Rect _containFit(Rect panel, ui.Image frame) {
  final scale = (panel.width / frame.width) < (panel.height / frame.height)
      ? panel.width / frame.width
      : panel.height / frame.height;
  return Rect.fromCenter(
    center: panel.center,
    width: frame.width * scale,
    height: frame.height * scale,
  );
}

/// The panel in global coordinates. [ZoomableFrame] fills the SizedBox it is
/// given, so the viewer is that box.
Rect _panelRect(WidgetTester tester) =>
    tester.getRect(find.byType(InteractiveViewer));

void _expectContainFit(
  WidgetTester tester,
  ui.Image frame,
  Size panel,
  String label,
) {
  final panelRect = _panelRect(tester);
  expect(panelRect.size, panel, reason: '$label panel');

  final drawn = tester.getRect(find.byType(RawImage));
  final want = _containFit(panelRect, frame);
  expect(drawn.width, closeTo(want.width, 0.5), reason: '$label width');
  expect(drawn.height, closeTo(want.height, 0.5), reason: '$label height');
  // Centred: equal slack left/right and top/bottom.
  expect(drawn.center.dx, closeTo(panelRect.center.dx, 0.5),
      reason: '$label cx');
  expect(drawn.center.dy, closeTo(panelRect.center.dy, 0.5),
      reason: '$label cy');
}

Future<void> _doubleTap(WidgetTester tester) async {
  final c = tester.getCenter(find.byType(InteractiveViewer));
  await tester.tapAt(c);
  await tester.pump(const Duration(milliseconds: 60));
  await tester.tapAt(c);
  await tester.pumpAndSettle();
}

void main() {
  const panel = Size(384, 500);

  // The reported symptom was a fixed scale (0.323 * frame width) shared by
  // every resolution. These pin each resolution to its own contain-fit.
  for (final (w, h) in [
    (1920, 1080),
    (1680, 1050),
    (1600, 900),
    (1366, 768),
    (1280, 720),
  ]) {
    testWidgets('$w x $h is drawn contain-fit and centred', (tester) async {
      final frame = await _mk(tester, w, h);
      addTearDown(frame.dispose);
      await _pump(tester, frame, panel: panel);
      _expectContainFit(tester, frame, panel, '$w x $h');
    });
  }

  // 1920x1080 must be byte-for-byte the pre-existing behaviour. This is the
  // strict requirement: at this size nothing may move.
  testWidgets('1920x1080 is unchanged: exactly 384x216, centred',
      (tester) async {
    final frame = await _mk(tester, 1920, 1080);
    addTearDown(frame.dispose);
    await _pump(tester, frame, panel: panel);

    final drawn = tester.getRect(find.byType(RawImage));
    final panelRect = _panelRect(tester);
    expect(drawn.size, const Size(384.0, 216.0));
    expect(drawn.center.dx, closeTo(panelRect.center.dx, 0.5));
    expect(drawn.center.dy, closeTo(panelRect.center.dy, 0.5));
    // Same ratio as the source, so the picture is not distorted.
    expect(drawn.width / drawn.height, closeTo(1920 / 1080, 0.001));
  });

  // A panel taller than the frame must clamp on height, not width.
  testWidgets('a wide-and-short panel fits by height', (tester) async {
    final frame = await _mk(tester, 1280, 720);
    addTearDown(frame.dispose);
    const short = Size(700, 300);
    await _pump(tester, frame, panel: short);
    _expectContainFit(tester, frame, short, '1280x720 in 700x300');
  });

  // The real complaint: the size must follow the frame, not stick at whatever
  // the first frame was.
  testWidgets('changing dimensions mid-session re-fits the frame',
      (tester) async {
    final a = await _mk(tester, 1920, 1080);
    final b = await _mk(tester, 1280, 720);
    final c = await _mk(tester, 1680, 1050);
    addTearDown(() {
      a.dispose();
      b.dispose();
      c.dispose();
    });

    await _pump(tester, a, panel: panel);
    _expectContainFit(tester, a, panel, '1920x1080');

    await _pump(tester, b, panel: panel);
    _expectContainFit(tester, b, panel, '1280x720');

    await _pump(tester, c, panel: panel);
    _expectContainFit(tester, c, panel, '1680x1050');

    await _pump(tester, a, panel: panel);
    _expectContainFit(tester, a, panel, '1920x1080 again');
  });

  // 1920x1080 and 1280x720 share a ratio, so a correct contain-fit draws them
  // identically. A viewer that pinned the scale would draw the second one
  // smaller; that is the arithmetic the report described.
  testWidgets('same-ratio frames of different sizes draw identically',
      (tester) async {
    final a = await _mk(tester, 1920, 1080);
    final b = await _mk(tester, 1280, 720);
    addTearDown(() {
      a.dispose();
      b.dispose();
    });

    await _pump(tester, a, panel: panel);
    final first = tester.getRect(find.byType(RawImage));
    await _pump(tester, b, panel: panel);
    final second = tester.getRect(find.byType(RawImage));

    expect(second.width, closeTo(first.width, 0.01));
    expect(second.height, closeTo(first.height, 0.01));
  });

  testWidgets('a ratio change resets zoom instead of leaving it offset',
      (tester) async {
    final wide = await _mk(tester, 1920, 1080);
    final tall = await _mk(tester, 1680, 1050);
    addTearDown(() {
      wide.dispose();
      tall.dispose();
    });

    await _pump(tester, wide, panel: panel);
    await _doubleTap(tester);
    expect(find.textContaining('✕'), findsOneWidget,
        reason: 'precondition: zoomed in');

    await _pump(tester, tall, panel: panel);
    expect(find.textContaining('✕'), findsNothing,
        reason: 'a different aspect ratio must return to the default view');
    _expectContainFit(tester, tall, panel, 'after reset');
  });

  testWidgets('a same-ratio size change keeps the zoom', (tester) async {
    final a = await _mk(tester, 1920, 1080);
    final b = await _mk(tester, 1280, 720);
    addTearDown(() {
      a.dispose();
      b.dispose();
    });

    await _pump(tester, a, panel: panel);
    await _doubleTap(tester);
    expect(find.textContaining('✕'), findsOneWidget);

    await _pump(tester, b, panel: panel);
    expect(find.textContaining('✕'), findsOneWidget,
        reason: '15fps of the same shape must not throw the zoom away');
  });
}