import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'l10n/strings.dart';
import 'screens/home_screen.dart';
import 'screens/pairing_screen.dart';
import 'state/providers.dart';
import 'theme/app_theme.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();

  // The app is a dark, full-bleed control surface, so the system bars are
  // forced dark rather than left to follow the wallpaper.
  SystemChrome.setSystemUIOverlayStyle(const SystemUiOverlayStyle(
    statusBarColor: Colors.transparent,
    statusBarIconBrightness: Brightness.light,
    systemNavigationBarColor: AppColors.background,
    systemNavigationBarIconBrightness: Brightness.light,
  ));

  runApp(const ProviderScope(child: SmartRemoteApp()));
}

/// The Smart Remote application.
///
/// The locale is watched from [langProvider] rather than left to the platform,
/// because the app stores its own language choice and offers an in-app toggle.
/// Building the [Directionality] here as well is what makes Arabic lay out
/// right-to-left: Flutter infers direction from the active locale, so a locale
/// the MaterialApp does not know about would silently render left-to-right.
class SmartRemoteApp extends ConsumerWidget {
  const SmartRemoteApp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final lang = ref.watch(langProvider);

    return MaterialApp(
      title: 'Smart Remote',
      debugShowCheckedModeBanner: false,
      theme: buildAppTheme(),
      locale: localeFor(lang),
      supportedLocales: const [Locale('en'), Locale('ar')],
      // Without this the app bundles Material's built-in English translations,
      // which would surface in system dialogs while the app's own text is
      // Arabic. Pinning the delegates to nil keeps every string in one place.
      localizationsDelegates: const [],
      home: const RootRouter(),
    );
  }
}

/// Chooses between pairing and the remote shell.
///
/// The pairing is the single source of truth for which screen is shown, so
/// unpairing from the status header lands back here and returns to the pairing
/// screen with no imperative navigation. That is what stops the app from being
/// stuck on a dead HomeScreen after the PC has been forgotten.
class RootRouter extends ConsumerStatefulWidget {
  const RootRouter({super.key});

  @override
  ConsumerState<RootRouter> createState() => _RootRouterState();
}

class _RootRouterState extends ConsumerState<RootRouter> {
  /// Completes once the saved pairing has been read from disk.
  late final Future<void> _restored = _restore();

  Future<void> _restore() async {
    await ref.read(pairingProvider.notifier).restore();
  }

  @override
  Widget build(BuildContext context) {
    final pairingState = ref.watch(pairingProvider);

    return FutureBuilder<void>(
      future: _restored,
      builder: (context, snapshot) {
        // The splash stays up until the stored pairing is known, otherwise the
        // app would flash the pairing screen at a user who is already paired.
        if (snapshot.connectionState != ConnectionState.done) {
          return const _SplashScreen();
        }

        return switch (pairingState.valueOrNull) {
          null => const PairingScreen(),
          _ => const HomeScreen(),
        };
      },
    );
  }
}

/// Shown while the saved pairing is being read.
class _SplashScreen extends StatelessWidget {
  const _SplashScreen();

  @override
  Widget build(BuildContext context) {
    return const Scaffold(
      body: Center(
        child: SizedBox(
          width: 32,
          height: 32,
          child: CircularProgressIndicator(color: AppColors.accent),
        ),
      ),
    );
  }
}
