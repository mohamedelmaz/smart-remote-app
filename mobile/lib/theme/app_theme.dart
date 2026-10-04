import 'package:flutter/material.dart';
import 'package:google_fonts/google_fonts.dart';

/// The Smart Remote visual identity.
///
/// Every colour, radius and duration in the app comes from here so the
/// palette cannot drift between screens.
class AppColors {
  const AppColors._();

  /// Deep void background.
  static const Color background = Color(0xFF0A0E1A);

  /// Electric cyan, used for accents, focus rings and active states.
  static const Color accent = Color(0xFF00E5FF);

  /// Translucent white for glassmorphic surfaces.
  static const Color glass = Color(0x0DFFFFFF); // 5% white
  static const Color glassStrong = Color(0x14FFFFFF); // 8% white
  static const Color glassBorder = Color(0x1AFFFFFF); // 10% white

  static const Color success = Color(0xFF00FF88);
  static const Color warning = Color(0xFFFF9500);
  static const Color error = Color(0xFFFF4444);

  static const Color textPrimary = Color(0xFFFFFFFF);
  static const Color textSecondary = Color(0xFF8899AA);
}

/// Shared geometry and motion tokens.
class AppTokens {
  const AppTokens._();

  static const double radius = 16;
  static const double radiusSmall = 12;
  static const double gap = 16;
  static const double gapSmall = 8;

  /// All interactive elements animate over this duration.
  static const Duration transition = Duration(milliseconds: 200);
  static const Curve curve = Curves.easeInOut;

  /// The neon glow applied to active elements.
  static List<BoxShadow> glow({double opacity = 0.35, double blur = 20}) => [
        BoxShadow(
          color: AppColors.accent.withValues(alpha: opacity),
          blurRadius: blur,
          spreadRadius: 1,
        ),
      ];

  /// A softer cyan glow for resting primary buttons.
  static List<BoxShadow> glowSubtle() => [
        BoxShadow(
          color: AppColors.accent.withValues(alpha: 0.16),
          blurRadius: 20,
        ),
      ];
}

/// Builds the app theme.
///
/// Inter is requested for text and JetBrains Mono for codes and numbers. Both
/// fall back to the platform font when the web font cannot be fetched, so the
/// UI never renders in a default serif or loses numeric alignment.
ThemeData buildAppTheme() {
  final base = ThemeData.dark(useMaterial3: true);

  return base.copyWith(
    scaffoldBackgroundColor: AppColors.background,
    colorScheme: const ColorScheme.dark(
      primary: AppColors.accent,
      onPrimary: AppColors.background,
      secondary: AppColors.accent,
      surface: AppColors.background,
      onSurface: AppColors.textPrimary,
      error: AppColors.error,
      onError: AppColors.textPrimary,
    ),
    textTheme: TextTheme(
      // Headings are Inter Bold.
      headlineMedium: interStyle(28, FontWeight.w700),
      headlineSmall: interStyle(24, FontWeight.w700),
      titleLarge: interStyle(20, FontWeight.w700),
      titleMedium: interStyle(16, FontWeight.w700),
      titleSmall: interStyle(14, FontWeight.w600),
      // Body is Inter Regular.
      bodyLarge: interStyle(16, FontWeight.w400),
      bodyMedium: interStyle(14, FontWeight.w400, color: AppColors.textSecondary),
      bodySmall: interStyle(12, FontWeight.w400, color: AppColors.textSecondary),
      labelLarge: interStyle(14, FontWeight.w600),
      labelMedium: interStyle(12, FontWeight.w600, color: AppColors.textSecondary),
    ),
    dividerColor: AppColors.glassBorder,
    splashColor: AppColors.accent.withValues(alpha: 0.08),
    highlightColor: AppColors.accent.withValues(alpha: 0.04),
    snackBarTheme: SnackBarThemeData(
      backgroundColor: const Color(0xFF141A2A),
      contentTextStyle: interStyle(14, FontWeight.w400),
      behavior: SnackBarBehavior.floating,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        side: const BorderSide(color: AppColors.glassBorder),
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: AppColors.glass,
      hintStyle: interStyle(15, FontWeight.w400, color: AppColors.textSecondary),
      labelStyle: interStyle(14, FontWeight.w600, color: AppColors.textSecondary),
      contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        borderSide: const BorderSide(color: AppColors.glassBorder),
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        borderSide: const BorderSide(color: AppColors.glassBorder),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        borderSide: const BorderSide(color: AppColors.accent, width: 2),
      ),
      errorBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        borderSide: const BorderSide(color: AppColors.error),
      ),
    ),
  );
}


/// Inter for UI text, with a system fallback if the font is unavailable.
///
/// The fallback matters: a phone that cannot reach fonts.gstatic.com must
/// still render readable text rather than failing to build a TextStyle.
TextStyle interStyle(double size, FontWeight weight,
    {Color color = AppColors.textPrimary}) {
  try {
    return GoogleFonts.inter(fontSize: size, fontWeight: weight, color: color);
  } catch (_) {
    return TextStyle(
      fontFamily: 'Inter',
      fontSize: size,
      fontWeight: weight,
      color: color,
      height: 1.4,
    );
  }
}

/// JetBrains Mono for codes, PINs and numbers.
///
/// Tabular figures keep a PIN or a frame counter from jittering as digits
/// change, which matters for a value the user reads at a glance.
TextStyle monoStyle(double size, FontWeight weight,
    {Color color = AppColors.textPrimary}) {
  try {
    return GoogleFonts.jetBrainsMono(
      fontSize: size,
      fontWeight: weight,
      color: color,
      fontFeatures: const [FontFeature.tabularFigures()],
    );
  } catch (_) {
    return TextStyle(
      fontFamily: 'JetBrains Mono',
      fontSize: size,
      fontWeight: weight,
      color: color,
      fontFeatures: const [FontFeature.tabularFigures()],
    );
  }
}
