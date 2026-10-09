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
