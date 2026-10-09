import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:smart_remote/core/commands.dart';
import 'package:smart_remote/core/remote_link.dart';
import 'package:smart_remote/screens/touchpad_screen.dart';
import 'package:smart_remote/state/providers.dart';

/// A link that records commands instead of opening a socket.
///
/// The touchpad's behaviour is entirely defined by the commands it emits, so
/// asserting on this list asserts on the contract with the PC - which is where
/// the context-menu bug was actually visible.
class _RecordingLink extends RemoteLink {
  _RecordingLink() : super(host: '127.0.0.1', port: 1);

  final List<Map<String, dynamic>> sent = [];

  @override
  LinkState get state => LinkState.ready;

  @override
  void send(Map<String, dynamic> message) => sent.add(message);

  @override
  Future<Ack> request(Map<String, dynamic> message,
      {Duration timeout = const Duration(seconds: 4)}) async {
    sent.add(message);
    return const Ack(ok: true);
  }

  /// Every `mouse.click` button emitted so far.
  List<String> get clicks => sent
      .where((m) => m['type'] == 'mouse.click')
      .map((m) => m['button'] as String)
      .toList();

  List<String> get types => sent.map((m) => m['type'] as String).toList();
}

void main() {
  late _RecordingLink link;

  Future<void> pumpPad(WidgetTester tester) async {
    link = _RecordingLink();
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          remoteLinkProvider.overrideWithValue(link),
          remoteProvider.overrideWithValue(Remote(link)),
        ],
        child: const MaterialApp(home: Scaffold(body: TouchpadScreen())),
      ),
    );
    await tester.pump();
  }

  /// A point inside the circular pad.
  ///
  /// Deliberately NOT the centre of the whole screen: the screen is a Column
  /// whose lower half is the button stack, so the widget centre lands on the
  /// Drag mode button rather than on the pad. A quarter of the way down is
  /// reliably inside the Expanded pad area at any test surface size.
  Offset padCentre(WidgetTester tester) {
    final rect = tester.getRect(find.byType(TouchpadScreen));
    return Offset(rect.center.dx, rect.top + rect.height * 0.25);
  }

  testWidgets('a single tap is a left click', (WidgetTester tester) async {
    await pumpPad(tester);

    await tester.tapAt(padCentre(tester));
    await tester.pump();

    expect(link.clicks, ['left']);
  });

  testWidgets('a fast swipe emits no click at all',
      (WidgetTester tester) async {
    // The regression this file exists for. A flick consumed most of its
    // distance as gesture slop before onScaleStart fired, so the recogniser
    // saw almost no travel and the tap branch classified the swipe as a click.
    await pumpPad(tester);
    final gesture = await tester.startGesture(padCentre(tester), pointer: 1);

    // One large jump, then lift straight away: the fastest possible flick.
    await gesture.moveBy(const Offset(120, 60));
    await gesture.up();
    await tester.pump();

    expect(link.clicks, isEmpty);
  });

  testWidgets('a slow drag emits no click at all',
      (WidgetTester tester) async {
    await pumpPad(tester);
    final gesture = await tester.startGesture(padCentre(tester), pointer: 1);

    for (var i = 0; i < 10; i++) {
      await gesture.moveBy(const Offset(6, 0));
      await tester.pump(const Duration(milliseconds: 16));
    }
    await gesture.up();
    await tester.pump();

    expect(link.clicks, isEmpty);
  });
  testWidgets('a phantom second pointer during a swipe is ignored',
      (WidgetTester tester) async {
    // The digitiser reports a spurious extra pointer for a frame or two as a
    // thumb slides. It must not be able to turn movement into a right click,
    // which is what opened the Windows context menu at random.
    await pumpPad(tester);
    final centre = padCentre(tester);
    final primary = await tester.startGesture(centre, pointer: 1);

    await primary.moveBy(const Offset(40, 0));

    // Phantom: lands and vanishes within a single frame.
    final phantom =
        await tester.startGesture(centre + const Offset(5, 5), pointer: 2);
    await tester.pump(const Duration(milliseconds: 8));
    await phantom.up();

    await primary.moveBy(const Offset(40, 0));
    await primary.up();
    await tester.pump(const Duration(milliseconds: 100));

    expect(link.clicks, isNot(contains('right')),
        reason: 'a sub-frame phantom pointer must never right click');
  });

  testWidgets('a sustained two-finger tap is a right click only when opted in',
      (WidgetTester tester) async {
    // The gesture is OFF by default so normal use never fires a system-wide
    // right click; the user must explicitly enable it first.
    await pumpPad(tester);
    await tester.tap(find.text('Two-finger right click: OFF'));
    await tester.pump();
    expect(find.text('Two-finger right click: ON'), findsOneWidget);

    final centre = padCentre(tester);
    final first = await tester.startGesture(centre, pointer: 1);
    final second =
        await tester.startGesture(centre + const Offset(60, 0), pointer: 2);

    // Held well past the dwell, then lifted without moving.
    await tester.pump(const Duration(milliseconds: 150));
    await first.up();
    await second.up();
    await tester.pump();

    expect(link.clicks, ['right']);
  });

  testWidgets('two-finger movement scrolls and never clicks',
      (WidgetTester tester) async {
    await pumpPad(tester);
    final centre = padCentre(tester);
    final first = await tester.startGesture(centre, pointer: 1);
    final second =
        await tester.startGesture(centre + const Offset(60, 0), pointer: 2);

    await tester.pump(const Duration(milliseconds: 80));
    await first.moveBy(const Offset(0, -60));
    await second.moveBy(const Offset(0, -60));
    await tester.pump();
    await first.up();
    await second.up();
    await tester.pump();

    expect(link.types, contains('mouse.scroll'));
    expect(link.clicks, isEmpty);
  });
  testWidgets('two-finger tap does NOT right click by default',
      (WidgetTester tester) async {
    // Safe-by-default: a sustained two-finger tap must not produce a right
    // click unless the user opted in. The explicit button stays available.
    await pumpPad(tester);
    expect(find.text('Two-finger right click: OFF'), findsOneWidget);

    final centre = padCentre(tester);
    final first = await tester.startGesture(centre, pointer: 1);
    final second =
        await tester.startGesture(centre + const Offset(60, 0), pointer: 2);
    await tester.pump(const Duration(milliseconds: 150));
    await first.up();
    await second.up();
    await tester.pump();

    expect(link.clicks, isNot(contains('right')));
  });

  testWidgets('an explicit Right click button always works',
      (WidgetTester tester) async {
    await pumpPad(tester);
    await tester.tap(find.text('Right click'));
    await tester.pump();

    expect(link.clicks, ['right']);
  });

  testWidgets('a new gesture never fires a spurious button-up',
      (WidgetTester tester) async {
    // The contract was deliberately changed: releaseInput() on EVERY touch
    // sent a stray right-button-up to Windows, which popped Chrome's context
    // menu mid-swipe. A gesture start must therefore emit nothing at all -
    // recovery lives in the "Release held buttons" action instead.
    await pumpPad(tester);

    final first = await tester.startGesture(padCentre(tester), pointer: 1);
    await first.moveBy(const Offset(60, 0));
    await tester.pump();
    await first.up();
    await tester.pump();

    link.sent.clear();

    final second = await tester.startGesture(padCentre(tester), pointer: 1);
    await second.moveBy(const Offset(60, 0));
    await tester.pump();
    await second.up();
    await tester.pump();

    expect(link.types.first, 'mouse.move',
        reason: 'a gesture may move the cursor, but must never open with a '
            'button command');
    expect(link.types, isNot(contains('input.release')),
        reason: 'release-on-every-touch is what opened the Windows context '
            'menu during a swipe');
    expect(link.types, isNot(contains('mouse.up')),
        reason: 'a stray button-up is exactly what reopens the context menu');
    expect(link.types, isNot(contains('mouse.down')));
  });

  testWidgets('a held drag is pressed and released cleanly',
      (WidgetTester tester) async {
    // The safety the old release-on-touch test was guarding: when a button is
    // deliberately held (Drag Mode), its mouse.down must always be matched by
    // a mouse.up when the gesture ends - never a stranded left button.
    await pumpPad(tester);
    await tester.tap(find.text('Drag mode: OFF'));
    await tester.pump();
    expect(find.text('Drag mode: ON'), findsOneWidget);

    final gesture = await tester.startGesture(padCentre(tester), pointer: 1);
    await gesture.moveBy(const Offset(40, 0));
    await tester.pump();
    await gesture.moveBy(const Offset(40, 0));
    await tester.pump();
    await gesture.up();
    await tester.pump();

    expect(link.types, contains('mouse.down'),
        reason: 'travel past the threshold in Drag Mode must hold the button');
    expect(link.types.last, 'mouse.up');
    expect(link.sent.last['button'], 'left');
    expect(link.clicks, isEmpty, reason: 'a drag must not also click');
  });

  testWidgets('input.release is sent by the explicit release action',
      (WidgetTester tester) async {
    // Stranded-button recovery is now a user action, not a per-gesture side
    // effect. This pins where the recovery command actually lives.
    await pumpPad(tester);

    await tester.tap(find.text('Release held buttons'));
    await tester.pump();

    expect(link.types, contains('input.release'));
  });
}