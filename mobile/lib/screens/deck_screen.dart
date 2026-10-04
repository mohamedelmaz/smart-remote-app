import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/commands.dart';
import '../models/models.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';

/// The macro deck: the server's stored actions as a grid of buttons.
///
/// Macros come from the PC rather than being hardcoded, so a user who edits
/// macros.json on the PC sees them here after a reload. Every macro carries a
/// text label; the icon is decorative and falls back to a neutral glyph when
/// the server sends a name this app does not recognise.
class DeckScreen extends ConsumerWidget {
  const DeckScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final macros = ref.watch(macrosProvider);
    final remote = ref.watch(remoteProvider);

    return RefreshIndicator(
      color: AppColors.accent,
      backgroundColor: const Color(0xFF141A2A),
      // Pull-to-refresh is the only way to pick up a macro edited on the PC, so
      // it has to work while the list is already on screen.
      onRefresh: () async {
        ref.invalidate(macrosProvider);
        await ref.read(macrosProvider.future);
      },
      child: macros.when(
        loading: () => const Center(
          child: CircularProgressIndicator(color: AppColors.accent),
        ),
        // Both the error and the stack trace are unused: the message below
        // already tells the user what to do, and dumping a trace into the UI
        // would only be noise.
        error: (_, _) => const _DeckMessage(
          icon: Icons.cloud_off,
          title: 'Could not load macros',
          detail: 'Pull down to try again.',
        ),
        data: (list) {
          if (list.isEmpty) {
            return const _DeckMessage(
              icon: Icons.grid_off,
              title: 'No macros on this PC',
              detail: 'The server returned an empty deck. Add macros to '
                  'macros.json in the Smart Remote config folder.',
            );
          }

          return GridView.builder(
            padding: const EdgeInsets.all(AppTokens.gap),
            // A max-extent grid keeps targets finger-sized on a phone and
            // avoids a cramped grid on a tablet.
            gridDelegate: const SliverGridDelegateWithMaxCrossAxisExtent(
              maxCrossAxisExtent: 180,
              mainAxisSpacing: AppTokens.gapSmall,
              crossAxisSpacing: AppTokens.gapSmall,
              childAspectRatio: 1.5,
            ),
            itemCount: list.length,
            itemBuilder: (context, index) {
              final macro = list[index];
              return MacroTile(
                macro: macro,
                enabled: remote?.connected ?? false,
                onTap: () => _runMacro(context, ref, macro),
              );
            },
          );
        },
      ),
    );
  }

  /// Runs a macro and reports a failure.
  ///
  /// The error is surfaced rather than swallowed: a macro that silently does
  /// nothing is indistinguishable from a broken remote.
  Future<void> _runMacro(
    BuildContext context,
    WidgetRef ref,
    Macro macro,
  ) async {
    final remote = ref.read(remoteProvider);
    // Not connected is a normal, recoverable state - the socket may still be
    // coming back - so it reports rather than silently doing nothing. Silently
    // returning here is what made the deck look frozen after a drop.
    if (remote == null) {
      _toast(context, 'Not connected to the PC.');
      return;
    }

    final ack = await remote.runMacro(macro.id);
    if (!context.mounted) return;
    if (ack.ok) {
      _toast(context, '${macro.label} ran');
    } else {
      _toast(context, ack.error.isEmpty ? 'Macro failed' : ack.error);
    }
  }

  /// Shows a brief message.
  ///
  /// Every message clears the previous one first: a rapid series of taps would
  /// otherwise queue several SnackBars, and the user would be reading a stale
  /// failure long after the command recovered.
  static void _toast(BuildContext context, String message) {
    final messenger = ScaffoldMessenger.of(context);
    messenger.hideCurrentSnackBar();
    messenger.showSnackBar(
      SnackBar(content: Text(message), duration: const Duration(seconds: 2)),
    );
  }
}
/// One macro button in the deck grid.
class MacroTile extends StatelessWidget {
  const MacroTile({
    super.key,
    required this.macro,
    required this.enabled,
    required this.onTap,
  });

  final Macro macro;
  final bool enabled;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      onTap: enabled ? onTap : null,
      padding: const EdgeInsets.all(12),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(
            macroIcon(macro.icon),
            size: 26,
            color: enabled ? AppColors.accent : AppColors.textSecondary,
          ),
          const SizedBox(height: AppTokens.gapSmall),
          // Flexible plus ellipsis: a long custom macro label must not overflow
          // a grid cell on a small phone.
          Flexible(
            child: Text(
              macro.label,
              maxLines: 2,
              textAlign: TextAlign.center,
              overflow: TextOverflow.ellipsis,
              style: interStyle(13, FontWeight.w600),
            ),
          ),
          if (macro.isShell) ...[
            const SizedBox(height: 4),
            Text(
              'command',
              style: monoStyle(
                10,
                FontWeight.w400,
                color: AppColors.textSecondary,
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// Maps a server icon name to a Material glyph.
///
/// An unrecognised name falls back to a neutral dot rather than crashing: the
/// icon is decorative and the label is what identifies the macro, so a bad
/// icon name must never stop a macro from being shown or run.
IconData macroIcon(String name) => switch (name) {
      'lock' => Icons.lock,
      'moon' => Icons.bedtime,
      'music' => Icons.music_note,
      'volume_off' => Icons.volume_off,
      'content_copy' => Icons.content_copy,
      'content_paste' => Icons.content_paste,
      'folder' => Icons.folder,
      'desktop' => Icons.desktop_windows,
      'monitor' => Icons.monitor_heart,
      'terminal' => Icons.terminal,
      'power' => Icons.power_settings_new,
      'camera' => Icons.photo_camera,
      'gamepad' => Icons.sports_esports,
      'settings' => Icons.settings,
      'browser' => Icons.travel_explore,
      _ => Icons.circle,
    };

/// A full-panel message used for the empty and error states.
class _DeckMessage extends StatelessWidget {
  const _DeckMessage({
    required this.icon,
    required this.title,
    required this.detail,
  });

  final IconData icon;
  final String title;
  final String detail;

  @override
  Widget build(BuildContext context) {
    // A scroll view keeps pull-to-refresh working: RefreshIndicator requires a
    // scrollable descendant even when there is nothing to scroll.
    return ListView(
      padding: const EdgeInsets.all(AppTokens.gap),
      children: [
        const SizedBox(height: 60),
        Icon(icon, size: 40, color: AppColors.textSecondary),
        const SizedBox(height: AppTokens.gap),
        Text(
          title,
          textAlign: TextAlign.center,
          style: interStyle(16, FontWeight.w700),
        ),
        const SizedBox(height: AppTokens.gapSmall),
        Text(
          detail,
          textAlign: TextAlign.center,
          style: interStyle(
            13,
            FontWeight.w400,
            color: AppColors.textSecondary,
          ),
        ),
      ],
    );
  }
}