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
