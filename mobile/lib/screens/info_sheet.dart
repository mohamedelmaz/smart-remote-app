import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../l10n/strings.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'pairing_screen.dart' show SectionTitle;

/// The version string shown in the About sheet.
///
/// Kept in step with `version:` in pubspec.yaml and `AppVersion` in
/// server/internal/remote/brand.go. It is a literal rather than a read of the
/// pubspec because the pubspec is not available to Dart code at runtime.
const String kAppVersion = '1.0.0';

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
      'assets/branding/smart-remote-logo.jpg',
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

/// "How to use" and "About", plus the language toggle.
///
/// The setup steps are deliberately the same three the server's own dashboard
/// prints, because that is where the user is looking when the PIN is fresh in
/// their hand and the phone is asking for an address.
class InfoSheet extends ConsumerWidget {
  const InfoSheet({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final lang = ref.watch(langProvider);
    final t = T(lang);

    return Scaffold(
      appBar: AppBar(
        title: Text(t.info),
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
                '${t.version} $kAppVersion',
                style: monoStyle(
                  12,
                  FontWeight.w400,
                  color: AppColors.textSecondary,
                ),
              ),
            ),
            const SizedBox(height: AppTokens.gap),

            // --- Language ---
            SectionTitle(label: t.language),
            const SizedBox(height: AppTokens.gapSmall),
            GlassPanel(
              child: Row(
                children: [
                  Expanded(
                    child: _LangChoice(
                      label: t.english,
                      code: 'EN',
                      selected: !lang.isRtl,
                      onTap: () =>
                          ref.read(langProvider.notifier).set(AppLang.en),
                    ),
                  ),
                  const SizedBox(width: AppTokens.gapSmall),
                  Expanded(
                    child: _LangChoice(
                      label: t.arabic,
                      code: 'ع',
                      selected: lang.isRtl,
                      onTap: () =>
                          ref.read(langProvider.notifier).set(AppLang.ar),
                    ),
                  ),
                ],
              ),
            ),
            const SizedBox(height: AppTokens.gap * 1.5),
            // --- How to use ---
            SectionTitle(label: t.howToUse),
            const SizedBox(height: AppTokens.gapSmall),
            GlassPanel(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  _Step(index: 1, title: t.step1Title, body: t.step1Body),
                  const SizedBox(height: AppTokens.gap),
                  _Step(index: 2, title: t.step2Title, body: t.step2Body),
                  const SizedBox(height: AppTokens.gap),
                  _Step(index: 3, title: t.step3Title, body: t.step3Body),
                ],
              ),
            ),
            const SizedBox(height: AppTokens.gap * 1.5),

            // --- About ---
            SectionTitle(label: t.aboutTitle),
            const SizedBox(height: AppTokens.gapSmall),
            GlassPanel(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(t.aboutBody, style: interStyle(13, FontWeight.w400)),
                  const SizedBox(height: AppTokens.gapSmall),
                  Text(
                    '${t.developedBy}: $kDeveloper',
                    style: monoStyle(
                      13,
                      FontWeight.w700,
                      color: AppColors.accent,
                    ),
                  ),
                  const SizedBox(height: 4),
                  Text(
                    t.license,
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

/// One numbered setup step.
class _Step extends StatelessWidget {
  const _Step({required this.index, required this.title, required this.body});

  final int index;
  final String title;
  final String body;

  @override
  Widget build(BuildContext context) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: 24,
          height: 24,
          alignment: Alignment.center,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            color: AppColors.accent.withValues(alpha: 0.18),
          ),
          child: Text(
            '$index',
            style: monoStyle(12, FontWeight.w700, color: AppColors.accent),
          ),
        ),
        const SizedBox(width: AppTokens.gapSmall),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(title, style: interStyle(14, FontWeight.w600)),
              const SizedBox(height: 2),
              Text(
                body,
                style: interStyle(
                  13,
                  FontWeight.w400,
                  color: AppColors.textSecondary,
                ),
              ),
            ],
          ),
        ),
      ],
    );
  }
}

/// One tappable language option.
class _LangChoice extends StatelessWidget {
  const _LangChoice({
    required this.label,
    required this.code,
    required this.selected,
    required this.onTap,
  });

  final String label;
  final String code;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
      child: Container(
        padding: const EdgeInsets.symmetric(vertical: 10, horizontal: 12),
        decoration: BoxDecoration(
          color: selected
              ? AppColors.accent.withValues(alpha: 0.16)
              : Colors.transparent,
          borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
          border: Border.all(
            color: selected ? AppColors.accent : AppColors.glassBorder,
          ),
        ),
        child: Row(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(code, style: monoStyle(14, FontWeight.w700)),
            const SizedBox(width: 8),
            Flexible(
              child: Text(
                label,
                style: interStyle(
                  13,
                  FontWeight.w600,
                  color: selected ? AppColors.accent : AppColors.textPrimary,
                ),
                overflow: TextOverflow.ellipsis,
              ),
            ),
          ],
        ),
      ),
    );
  }
}