import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:smart_remote/main.dart';
import 'package:smart_remote/screens/info_sheet.dart';

/// The app is English-only: the info sheet must not offer a language choice,
/// and the app must start with no language UI anywhere.
void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  Future<void> pumpInfo(WidgetTester tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: MaterialApp(home: InfoSheet())),
    );
    await tester.pumpAndSettle();
  }

  testWidgets('info sheet shows no language section',
      (WidgetTester tester) async {
    await pumpInfo(tester);

    expect(find.text('Language'), findsNothing);
    expect(find.text('English'), findsNothing);
    expect(find.text('العربية'), findsNothing);
    expect(find.text('EN'), findsNothing);
  });

  testWidgets('info sheet still shows the rest of its content',
      (WidgetTester tester) async {
    // A tall viewport so the whole sheet lays out without scrolling: the
    // assertions below are about which sections exist, not about scrolling.
    tester.view.physicalSize = const Size(1000, 2400);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    await pumpInfo(tester);

    expect(find.byType(BrandLogo), findsOneWidget);
    expect(find.text('Smart Remote'), findsOneWidget);
    expect(find.textContaining('Version'), findsOneWidget);
    // SectionTitle upper-cases whatever label it is given.
    expect(find.text('HOW TO USE'), findsOneWidget);
    expect(find.text('ABOUT'), findsOneWidget);
  });

  testWidgets('the app starts with no language UI', (WidgetTester tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: SmartRemoteApp()),
    );
    await tester.pumpAndSettle();

    expect(find.text('Language'), findsNothing);
    expect(find.text('English'), findsNothing);
    expect(find.text('العربية'), findsNothing);
  });
}