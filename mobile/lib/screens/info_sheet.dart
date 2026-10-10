import 'package:flutter/material.dart';

import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'pairing_screen.dart' show SectionTitle;

/// The version string shown in the About sheet.
///
/// Kept in step with `version:` in pubspec.yaml and `AppVersion` in
/// server/internal/remote/brand.go. It is a literal rather than a read of the
/// pubspec because the pubspec is not available to Dart code at runtime.
const String kAppVersion = '1.0.3';

/// The author shown in the About sheet.
///
/// Kept in step with `developer:` in pubspec.yaml and `Developer` in
/// server/internal/remote/brand.go.
const String kDeveloper = 'elmamo';

/// Opens the [InfoSheet].
///
/// Exposed as a function rather than a widget so both the pairing and home
/// screens can place an info button in their own layout without duplicating the
/// navigation call.
void showInfoSheet(BuildContext context) {
  Navigator.of(context).push(
    MaterialPageRoute<void>(
      builder: (_) => const InfoSheet(),
      fullscreenDialog: true,
    ),
  );
}

/// The brand logo, shown on the pairing screen and at the top of this sheet.
///
/// Registered in pubspec.yaml under assets/branding. The [errorBuilder] keeps a
/// missing or corrupt asset from taking down the sheet: the mark is decorative,
/// and an absent image must not hide the About text and the setup steps, which
/// are the parts that actually matter.
class BrandLogo extends StatelessWidget {
  const BrandLogo({super.key, this.size = 96});

  final double size;

  @override
  Widget build(BuildContext context) {
    return Image.asset(
      'assets/branding/logo.png',
      width: size,
      height: size,
      fit: BoxFit.contain,
      errorBuilder: (context, error, stack) => SizedBox(
        width: size,
        height: size,
        child: DecoratedBox(
          decoration: BoxDecoration(
            color: AppColors.accent.withValues(alpha: 0.15),
            borderRadius: BorderRadius.circular(AppTokens.radius),
          ),
          child: Icon(
            Icons.control_camera,
            size: size * 0.5,
            color: AppColors.accent,
          ),
        ),
      ),
    );
  }
}

/// "How to use" and "About".
///
/// The setup steps are deliberately the same three the server's own dashboard
/// prints, because that is where the user is looking when the PIN is fresh in
/// their hand and the phone is asking for an address.
class InfoSheet extends StatelessWidget {
  const InfoSheet({super.key});

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Info'),
        backgroundColor: AppColors.background,
        elevation: 0,
      ),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.all(AppTokens.gap),
          children: [
            const Center(child: BrandLogo()),
            const SizedBox(height: AppTokens.gap),
            Center(
              child: Text(
                'Smart Remote',
                style: interStyle(22, FontWeight.w700),
              ),
            ),
            const SizedBox(height: AppTokens.gapSmall),
            Center(
              child: Text(
                'Version $kAppVersion',
                style: monoStyle(
                  12,
                  FontWeight.w400,
                  color: AppColors.textSecondary,
                ),
              ),
            ),
            const SizedBox(height: AppTokens.gap * 1.5),
            // --- How to use ---
            const SectionTitle(label: 'How to use'),
            const SizedBox(height: AppTokens.gapSmall),
            GlassPanel(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    '1. Run the server on the computer.\n'
                    '2. Make sure both devices are connected to the same Wi-Fi network.\n'
                    '3. Enter the IP address and PIN to connect.',
                    style: interStyle(
                      13,
                      FontWeight.w400,
                      color: AppColors.textSecondary,
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(height: AppTokens.gap * 1.5),

            // --- About ---
            const SectionTitle(label: 'About'),
            const SizedBox(height: AppTokens.gapSmall),
            GlassPanel(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    'Smart Remote Control v$kAppVersion. Developed by elmamo. '
                    'A professional, zero-latency LAN remote control solution.',
                    style: interStyle(13, FontWeight.w400)),
                  const SizedBox(height: AppTokens.gapSmall),
                  Text(
                    'Developed by: $kDeveloper',
                    style: monoStyle(
                      13,
                      FontWeight.w700,
                      color: AppColors.accent,
                    ),
                  ),
                  const SizedBox(height: 4),
                  Text(
                    'Released under the MIT License.',
                    style: interStyle(
                      12,
                      FontWeight.w400,
                      color: AppColors.textSecondary,
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(height: AppTokens.gap),
          ],
        ),
      ),
    );
  }
}
