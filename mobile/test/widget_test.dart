import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:smart_remote/main.dart';
import 'package:smart_remote/models/pairing.dart';
import 'package:smart_remote/screens/keyboard_screen.dart';
import 'package:smart_remote/theme/app_theme.dart';

void main() {
  // Every test starts with empty storage: the real app persists a pairing, and
  // a leftover value from another test would silently change which screen the
  // router shows.
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  testWidgets('shows the pairing screen when no PC is saved',
      (WidgetTester tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: SmartRemoteApp()),
    );

    // The router waits for the stored pairing before choosing a screen, so the
    // first frame is the splash and the pairing screen follows.
    await tester.pumpAndSettle();

    expect(find.text('Smart Remote'), findsOneWidget);
    expect(find.text('Connect'), findsOneWidget);
  });

  testWidgets('applies the dark app theme', (WidgetTester tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: SmartRemoteApp()),
    );
    await tester.pumpAndSettle();

    final context = tester.element(find.text('Smart Remote'));
    final theme = Theme.of(context);

    expect(theme.scaffoldBackgroundColor, AppColors.background);
    expect(theme.colorScheme.primary, AppColors.accent);
  });

  testWidgets('renders the saved pairing when one exists',
      (WidgetTester tester) async {
    // A saved pairing must route straight to the remote shell, otherwise a
    // paired user would have to re-enter the PIN on every launch.
    SharedPreferences.setMockInitialValues({
      'smart_remote.pairing': 'host=192.168.1.10&port=9520&pin=123456',
    });

    await tester.pumpWidget(
      const ProviderScope(child: SmartRemoteApp()),
    );
    await tester.pumpAndSettle();

    // The status header shows the paired PC, not the pairing form.
    expect(find.text('192.168.1.10'), findsOneWidget);
    expect(find.text('Pad'), findsOneWidget);
  });

  group('Pairing persistence', () {
    test('round-trips through SharedPreferences', () async {
      final prefs = await SharedPreferences.getInstance();
      const pairing = Pairing(
        host: '10.0.0.5',
        port: 9520,
        pin: '654321',
        label: 'Office PC',
      );

      await pairing.save(prefs);
      final loaded = Pairing.load(prefs);

      expect(loaded, isNotNull);
      expect(loaded!.host, '10.0.0.5');
      expect(loaded.port, 9520);
      expect(loaded.pin, '654321');
      expect(loaded.label, 'Office PC');
    });

    test('treats a corrupt entry as absent', () async {
      // A bad write must not permanently block the user from the app.
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString('smart_remote.pairing', '%%%%not-a-query%%%%');

      expect(Pairing.load(prefs), isNull);
    });

    test('clear removes the stored pairing', () async {
      final prefs = await SharedPreferences.getInstance();
      const pairing = Pairing(host: '10.0.0.5', port: 9520, pin: '111111');

      await pairing.save(prefs);
      await Pairing.clear(prefs);

      expect(Pairing.load(prefs), isNull);
    });
  });

  group('NativeTextDiff', () {
    // Applies a diff the way the PC would, so a wrong plan shows up as visibly
    // wrong text rather than as an implementation detail.
    String apply(String pc, NativeTextDiff plan) {
      var out = pc;
      for (var i = 0; i < plan.backspaces; i++) {
        if (out.isNotEmpty) {
          out = out.substring(0, out.length - 1);
        }
      }
      return out + plan.text;
    }

    test('deleting one character erases it and retypes nothing', () {
      // THE REPORTED BUG. Deleting the "o" from "hello" used to send one
      // Backspace *and* the text "hell", so the PC ended up with "hellhell" -
      // the text repeated instead of disappearing. A pure deletion must send
      // backspaces and no text at all.
      final plan = NativeTextDiff('hello', 'hell');

      expect(plan.backspaces, 1);
      expect(plan.text, isEmpty,
          reason: 'a pure deletion must not retype the surviving text');
      expect(apply('hello', plan), 'hell');
    });

    test('deleting several characters at once erases exactly those', () {
      final plan = NativeTextDiff('hello world', 'hello');

      expect(plan.backspaces, 6);
      expect(plan.text, isEmpty);
      expect(apply('hello world', plan), 'hello');
    });

    test('clearing the whole field is a run of backspaces', () {
      final plan = NativeTextDiff('anything', '');

      expect(plan.backspaces, 8);
      expect(plan.text, isEmpty);
      expect(apply('anything', plan), '');
    });

    test('typing appends without erasing', () {
      final plan = NativeTextDiff('hel', 'hello');

      expect(plan.backspaces, 0);
      expect(plan.text, 'lo');
      expect(apply('hel', plan), 'hello');
    });

    test('first keystroke on an empty field is a pure append', () {
      final plan = NativeTextDiff('', 'a');

      expect(plan.isNoop, isFalse);
      expect(plan.backspaces, 0);
      expect(plan.text, 'a');
    });

    test('an edit in the middle rewinds and retypes only the tail', () {
      // "hello" -> "help": the cursor was inside the word, so the "lo" has to be
      // erased and "p" typed. Retyping the whole word would duplicate text.
      final plan = NativeTextDiff('hello', 'help');

      expect(plan.backspaces, 2);
      expect(plan.text, 'p');
      expect(apply('hello', plan), 'help');
    });

    test('autocorrect replacing a word retypes only what changed', () {
      // The phone keyboard rewrites the whole field on autocorrect, which is
      // the case that made the original bug fire constantly in practice.
      final plan = NativeTextDiff('teh', 'the');

      expect(apply('teh', plan), 'the');
    });

    test('an unchanged field sends nothing at all', () {
      expect(NativeTextDiff('same', 'same').isNoop, isTrue);
    });

    test('every plan leaves the PC holding exactly the new text', () {
      // The invariant behind all of the above: replaying the plan against what
      // the PC already had must reproduce the field, for any pair of strings.
      const cases = <List<String>>[
        ['', 'a'],
        ['a', ''],
        ['hello', 'hell'],
        ['hello', 'hello world'],
        ['hello world', 'hello'],
        ['teh', 'the'],
        ['abc', 'axc'],
        ['same', 'same'],
        ['', ''],
        ['one two three', 'one three'],
      ];

      for (final pair in cases) {
        final plan = NativeTextDiff(pair[0], pair[1]);
        expect(apply(pair[0], plan), pair[1],
            reason: 'NativeTextDiff(${pair[0]}, ${pair[1]}) produced '
                '${plan.backspaces} backspaces and "${plan.text}"');
      }
    });
  });

  group('Keyboard mode toggle', () {
    // Mounted unpaired on purpose: the toggle is pure local UI state, so it
    // must work with no PC reachable. If it only functioned while paired, a
    // user whose PC had dropped would have no way to reach the phone keyboard.
    Future<void> pumpKeyboard(WidgetTester tester) async {
      await tester.pumpWidget(
        const ProviderScope(child: MaterialApp(home: KeyboardScreen())),
      );
      await tester.pump();
    }

    testWidgets('starts on the app keyboard and offers both modes',
        (WidgetTester tester) async {
      await pumpKeyboard(tester);

      expect(find.text('App keyboard'), findsOneWidget);
      expect(find.text('Phone keyboard'), findsOneWidget);
      // The virtual grid is the default, so its keys are on screen.
      expect(find.text('Space'), findsOneWidget);
      // No native field until it is asked for.
      expect(find.byType(TextField), findsNothing);
    });

    testWidgets('switching to the phone keyboard shows a text field',
        (WidgetTester tester) async {
      await pumpKeyboard(tester);

      await tester.tap(find.text('Phone keyboard'));
      await tester.pumpAndSettle();

      // The system keyboard only attaches to a real input field, so the field
      // appearing is what makes the phone keyboard possible at all.
      expect(find.byType(TextField), findsOneWidget);
      // The grid deliberately stays: its named keys and modifier staging are
      // still useful while typing on the phone.
      expect(find.text('Space'), findsOneWidget);
    });

    testWidgets('switching back restores the app keyboard',
        (WidgetTester tester) async {
      await pumpKeyboard(tester);

      await tester.tap(find.text('Phone keyboard'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('App keyboard'));
      await tester.pumpAndSettle();

      expect(find.text('Space'), findsOneWidget);
      expect(find.byType(TextField), findsNothing);
    });
  });
}
