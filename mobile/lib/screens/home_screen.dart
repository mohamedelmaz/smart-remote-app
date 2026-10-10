import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../state/providers.dart';
import '../theme/app_theme.dart';
import 'deck_screen.dart';
import 'info_sheet.dart';
import 'keyboard_screen.dart';
import 'media_screen.dart';
import 'screen_viewer_screen.dart';
import 'touchpad_screen.dart';
import 'voice_screen.dart';
import 'webcam_screen.dart';

/// The tabs in the bottom navigation, in order.
enum RemoteTab {
  pad('Pad', Icons.control_camera_outlined, Icons.control_camera),
  media('Media', Icons.play_circle_outline, Icons.play_circle),
  deck('Deck', Icons.grid_view_outlined, Icons.grid_view),
  keys('Keys', Icons.keyboard_outlined, Icons.keyboard),
  screen('Screen', Icons.desktop_windows_outlined, Icons.desktop_windows),
  webcam('Webcam', Icons.photo_camera_outlined, Icons.photo_camera),
  voice('Voice', Icons.mic_none, Icons.mic);

  const RemoteTab(this.label, this.icon, this.activeIcon);

  /// The visible label. Always shown, never icon-only.
  final String label;
  final IconData icon;
  final IconData activeIcon;
}

/// The paired home shell: a status header, the active tab and bottom nav.
class HomeScreen extends ConsumerStatefulWidget {
  const HomeScreen({super.key});

  @override
  ConsumerState<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends ConsumerState<HomeScreen> {
  RemoteTab _tab = RemoteTab.pad;

  @override
  Widget build(BuildContext context) {
    final pairing = ref.watch(pairingProvider).valueOrNull;

    // The app is only reachable after pairing, but a defensive redirect
    // avoids a black screen if the pairing is cleared from elsewhere.
    if (pairing == null) {
      return const Scaffold(
        body: Center(
          child: CircularProgressIndicator(color: AppColors.accent),
        ),
      );
    }

    return Scaffold(
      body: SafeArea(
        child: Column(
          children: [
            StatusHeader(label: pairing.displayName),
            Expanded(child: _bodyFor(_tab)),
            BottomNav(
              current: _tab,
              onChanged: (tab) => setState(() => _tab = tab),
            ),
          ],
        ),
      ),
    );
  }

  Widget _bodyFor(RemoteTab tab) {
    switch (tab) {
      case RemoteTab.pad:
        return const TouchpadScreen();
      case RemoteTab.media:
        return const MediaScreen();
      case RemoteTab.deck:
        return const DeckScreen();
      case RemoteTab.keys:
        return const KeyboardScreen();
      case RemoteTab.screen:
        return const ScreenViewerScreen();
      case RemoteTab.webcam:
        return const WebcamScreen();
      case RemoteTab.voice:
        return const VoiceScreen();
    }
  }
}


/// The header showing the paired PC and live connection state.
class StatusHeader extends ConsumerWidget {
  const StatusHeader({super.key, required this.label});

  final String label;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final link = ref.watch(remoteLinkProvider);
    final status = ref.watch(serverStatusProvider).valueOrNull;

    // The link's own state stream drives the indicator, so the header reacts
    // the instant the socket drops rather than on the next poll.
    return StreamBuilder<String>(
      stream: link == null
          ? const Stream<String>.empty()
          : link.states.map((s) => s.name),
      builder: (context, snapshot) {
        final stateName = snapshot.data ?? link?.state.name ?? 'idle';
        final (color, text) = switch (stateName) {
          'ready' => (AppColors.success, 'Connected'),
          'connecting' => (AppColors.warning, 'Pairing…'),
          'reconnecting' => (AppColors.warning, 'Reconnecting…'),
          'failed' => (AppColors.error, 'PIN rejected'),
          _ => (AppColors.error, 'Offline'),
        };

        return Padding(
          padding: const EdgeInsets.fromLTRB(
              AppTokens.gap, AppTokens.gapSmall, AppTokens.gap, 0),
          child: Row(
            children: [
              Container(
                width: 8,
                height: 8,
                decoration: BoxDecoration(
                  shape: BoxShape.circle,
                  color: color,
                  boxShadow: [BoxShadow(color: color, blurRadius: 8)],
                ),
              ),
              const SizedBox(width: AppTokens.gapSmall),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(label,
                        style: interStyle(15, FontWeight.w700),
                        overflow: TextOverflow.ellipsis),
                    // The state is spelled out in words as well as colour.
                    Text(text,
                        style: interStyle(12, FontWeight.w400,
                            color: AppColors.textSecondary)),
                  ],
                ),
              ),
              if (status != null && status.inputBlocked)
                const Icon(Icons.warning_amber_rounded,
                    color: AppColors.warning, size: 20),
              IconButton(
                tooltip: 'Info',
                icon: const Icon(Icons.info_outline, size: 20),
                color: AppColors.textSecondary,
                onPressed: () => showInfoSheet(context),
              ),
              IconButton(
                // An explicit retry. The link does eventually reconnect on its
                // own, but a user staring at "PIN rejected" or "Offline" had no
                // way to ask for another attempt - the only option was
                // force-closing the app, which is precisely the behaviour this
                // bug produced.
                tooltip: 'Reconnect now',
                icon: const Icon(Icons.refresh, size: 20),
                color: stateName == 'ready'
                    ? AppColors.textSecondary
                    : AppColors.accent,
                onPressed: link?.retryNow,
              ),
              IconButton(
                tooltip: 'Rename this phone',
                icon: const Icon(Icons.edit_outlined, size: 20),
                color: AppColors.textSecondary,
                onPressed: () => showRenameDialog(context, ref),
              ),
              IconButton(
                tooltip: 'Unpair this PC',
                icon: const Icon(Icons.logout, size: 20),
                color: AppColors.textSecondary,
                onPressed: () =>
                    ref.read(pairingProvider.notifier).unpair(),
              ),
            ],
          ),
        );
      },
    );
  }
}

/// Renames this phone as shown on the PC dashboard device list.
///
/// Saving persists the name and reconnects so the dashboard shows it
/// immediately. Clearing the field removes the override and the phone falls
/// back to its OS-reported name. The dialog owns its controller and disposes
/// it on close, so no state leaks past the dialog's lifetime.
Future<void> showRenameDialog(BuildContext context, WidgetRef ref) async {
  final notifier = ref.read(pairingProvider.notifier);
  final ctrl = TextEditingController(text: notifier.deviceName);
  try {
    final save = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Rename this phone'),
        content: TextField(
          controller: ctrl,
          autofocus: true,
          autocorrect: false,
          maxLength: 64,
          decoration: const InputDecoration(
            labelText: 'Device name (optional)',
            hintText: "e.g. Ahmed's phone",
            counterText: '',
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(ctx).pop(false),
            child: const Text('Close'),
          ),
          TextButton(
            onPressed: () => Navigator.of(ctx).pop(true),
            child: const Text('Save'),
          ),
        ],
      ),
    );
    if (save == true && context.mounted) {
      await notifier.setDeviceName(ctrl.text);
      // Re-announce with the new name; user-initiated, so the brief
      // reconnect blip is expected rather than surprising.
      await notifier.reconnect();
    }
  } finally {
    ctrl.dispose();
  }
}

/// The bottom navigation bar.
class BottomNav extends StatelessWidget {
  const BottomNav({super.key, required this.current, required this.onChanged});

  final RemoteTab current;
  final ValueChanged<RemoteTab> onChanged;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 4),
      decoration: const BoxDecoration(
        color: AppColors.glass,
        border: Border(top: BorderSide(color: AppColors.glassBorder)),
      ),
      child: Row(
        mainAxisAlignment: MainAxisAlignment.spaceEvenly,
        children: [
          for (final tab in RemoteTab.values)
            _NavItem(
              tab: tab,
              selected: tab == current,
              onTap: () => onChanged(tab),
            ),
        ],
      ),
    );
  }
}

/// One navigation item: an icon with a persistent label underneath.
class _NavItem extends StatelessWidget {
  const _NavItem({
    required this.tab,
    required this.selected,
    required this.onTap,
  });

  final RemoteTab tab;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final color = selected ? AppColors.accent : AppColors.textSecondary;

    return Expanded(
      child: Semantics(
        button: true,
        selected: selected,
        label: tab.label,
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
          child: Padding(
            padding: const EdgeInsets.symmetric(vertical: 6),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                AnimatedContainer(
                  duration: AppTokens.transition,
                  padding: const EdgeInsets.symmetric(
                      horizontal: 14, vertical: 6),
                  decoration: BoxDecoration(
                    color: selected
                        ? AppColors.accent.withValues(alpha: 0.16)
                        : Colors.transparent,
                    borderRadius: BorderRadius.circular(999),
                    boxShadow: selected ? AppTokens.glowSubtle() : null,
                  ),
                  child: Icon(
                    selected ? tab.activeIcon : tab.icon,
                    size: 20,
                    color: color,
                  ),
                ),
                const SizedBox(height: 4),
                Text(
                  tab.label,
                  style: interStyle(10, FontWeight.w600, color: color),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
