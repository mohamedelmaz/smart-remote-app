import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'package:smart_remote/screens/info_sheet.dart';
import 'package:smart_remote/screens/pairing_screen.dart';

/// The pairing header must mirror the info sheet: brand mark and title
/// centered, info action pinned to the top corner.
void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  Future<void> pumpPairing(WidgetTester tester) async {
    await tester.pumpWidget(
      const ProviderScope(child: MaterialApp(home: PairingScreen())),
    );
    await tester.pumpAndSettle();
  }

  testWidgets('brand logo and title are horizontally centered',
      (WidgetTester tester) async {
    await pumpPairing(tester);

    expect(find.byType(BrandLogo), findsOneWidget);
    expect(find.text('Smart Remote'), findsOneWidget);

    final screenWidth = tester.view.physicalSize.width /
        tester.view.devicePixelRatio;
    const margin = 8.0;

    final logoCenter =
        tester.getCenter(find.byType(BrandLogo)).dx;
    expect(logoCenter, moreOrLessEquals(screenWidth / 2, epsilon: margin));

    final titleCenter =
        tester.getCenter(find.text('Smart Remote')).dx;
    expect(titleCenter, moreOrLessEquals(screenWidth / 2, epsilon: margin));
  });

  testWidgets('logo stays centered when the description fits one line',
      (WidgetTester tester) async {
    // On a wide screen the sentence no longer wraps, so the header column
    // shrink-wraps to its content instead of filling the Stack. Only
    // Stack.alignment: topCenter keeps that narrower column centered; with
    // the default topStart it would drift to the start edge.
    tester.view.physicalSize = const Size(1600, 900);
    tester.view.devicePixelRatio = 1.0;
    addTearDown(tester.view.reset);

    await pumpPairing(tester);

    // Structural guard: the header Stack must pin its column to topCenter.
    // Behavioural assertions below can survive a regression here only while
    // the column fills the Stack, so the property itself is checked too.
    final headerStack = find
        .ancestor(
          of: find.text('Find your PC, then use the PIN it shows on its '
              'dashboard.'),
          matching: find.byType(Stack),
        )
        .first;
    final stack = tester.widget<Stack>(headerStack);
    expect(stack.alignment, Alignment.topCenter);

    final logoCenter = tester.getCenter(find.byType(BrandLogo)).dx;
    expect(logoCenter, moreOrLessEquals(800, epsilon: 8));

    final titleCenter = tester.getCenter(find.text('Smart Remote')).dx;
    expect(titleCenter, moreOrLessEquals(800, epsilon: 8));
  });

  testWidgets('info button is present and opens the sheet',
      (WidgetTester tester) async {
    await pumpPairing(tester);

    final infoButton = find.widgetWithIcon(IconButton, Icons.info_outline);
    expect(infoButton, findsOneWidget);

    await tester.tap(infoButton);
    await tester.pumpAndSettle();

    expect(find.byType(InfoSheet), findsOneWidget);
  });
}
