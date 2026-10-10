import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/commands.dart';
import '../core/remote_link.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'pairing_screen.dart';

/// One desktop shortcut, expressed as the chord Windows itself uses.
///
/// [mods] are held down, [key] is tapped, then the modifiers are released -
/// which is exactly what the server's Chord does in one atomic SendInput batch.
/// Sending them as a single chord rather than as separate press/tap/release
/// commands is what guarantees no modifier is ever left latched on the PC.
class _Shortcut {
  const _Shortcut({
    required this.en,
    required this.icon,
    required this.mods,
    required this.key,
  });

  final String en;
  final IconData icon;
  final List<String> mods;
  final String key;
}

/// The shortcuts offered on this screen.
///
/// Modifiers use the protocol's own names ("win", "ctrl", "alt", "shift"), so
/// no virtual-key guessing happens here; the server resolves them against the
/// active Windows keyboard layout.
///
/// [mods] must contain ONLY modifiers. The chord the server sends is
/// "mods down, key down+up, mods up", so listing the chord key inside [mods] as
/// well makes it get pressed twice and released twice - Win+D would be sent as
/// Win down, D down, D tap, D up, Win up, which Windows reads as an autorepeat
/// and not a second chord. That is why the trigger key appears only in [key].
const List<_Shortcut> _shortcutSpecs = [
  _Shortcut(
    en: 'Show Desktop',
    icon: Icons.desktop_windows,
    mods: ['win'],
    key: 'd',
  ),
  _Shortcut(
    en: 'File Explorer',
    icon: Icons.folder_open,
    mods: ['win'],
    key: 'e',
  ),
  _Shortcut(
    en: 'Lock PC',
    icon: Icons.lock,
    mods: ['win'],
    key: 'l',
  ),
  _Shortcut(
    en: 'Switch Apps',
    icon: Icons.swap_horiz,
    mods: ['alt'],
    key: 'tab',
  ),
  _Shortcut(
    en: 'Task Manager',
    icon: Icons.speed,
    mods: ['ctrl', 'shift'],
    key: 'esc',
  ),
  _Shortcut(
    en: 'Snipping Tool',
    icon: Icons.crop_free,
    mods: ['win', 'shift'],
    key: 's',
  ),
  _Shortcut(
    en: 'Copy',
    icon: Icons.content_copy,
    mods: ['ctrl'],
    key: 'c',
  ),
  _Shortcut(
    en: 'Paste',
    icon: Icons.content_paste,
    mods: ['ctrl'],
    key: 'v',
  ),
  _Shortcut(
    en: 'Cut',
    icon: Icons.content_cut,
    mods: ['ctrl'],
    key: 'x',
  ),
  _Shortcut(
    en: 'Undo',
    icon: Icons.undo,
    mods: ['ctrl'],
    key: 'z',
  ),
  _Shortcut(
    en: 'Redo',
    icon: Icons.redo,
    mods: ['ctrl'],
    key: 'y',
  ),
  _Shortcut(
    en: 'Find',
    icon: Icons.search,
    mods: ['ctrl'],
    key: 'f',
  ),
  _Shortcut(
    en: 'Refresh',
    icon: Icons.refresh,
    // F5 needs no modifier. It is sent as a chord with an empty modifier list
    // rather than as a tap so every shortcut goes through one code path - and
    // Chord already handles that case, sending just the key down and up.
    mods: [],
    key: 'f5',
  ),
  _Shortcut(
    en: 'Settings',
    icon: Icons.settings,
    mods: ['win'],
    key: 'i',
  ),
  _Shortcut(
    en: 'Run',
    icon: Icons.terminal,
    mods: ['win'],
    key: 'r',
  ),
  _Shortcut(
    en: 'Quick Assist',
    icon: Icons.support_agent,
    mods: ['win'],
    key: 'q',
  ),
  _Shortcut(
    en: 'Project',
    icon: Icons.video_camera_front,
    mods: ['ctrl'],
    key: 'p',
  ),
  _Shortcut(
    en: 'Close Window',
    icon: Icons.close,
    mods: ['alt'],
    key: 'f4',
  ),
  _Shortcut(
    en: 'Rename',
    icon: Icons.drive_file_rename_outline,
    mods: [],
    key: 'f2',
  ),
  _Shortcut(
    en: 'Save',
    icon: Icons.save,
    mods: ['ctrl'],
    key: 's',
  ),
  _Shortcut(
    en: 'Search',
    icon: Icons.search,
    mods: ['win'],
    key: 's',
  ),
  _Shortcut(
    en: 'Action Center',
    icon: Icons.notifications,
    mods: ['win'],
    key: 'a',
  ),
  _Shortcut(
    en: 'Active Apps',
    icon: Icons.apps,
    mods: ['win'],
    key: 'tab',
  ),
];

/// Media transport, volume and quick desktop shortcuts.
class MediaScreen extends ConsumerStatefulWidget {
  const MediaScreen({super.key});

  @override
  ConsumerState<MediaScreen> createState() => _MediaScreenState();
}

class _MediaScreenState extends ConsumerState<MediaScreen> {
  double _volume = 60;

  /// Debounces the volume slider so a drag does not flood the socket with
  /// one command per frame.
  Timer? _volumeDebounce;
  bool _muted = false;
  bool _commandInProgress = false;

  @override
  void dispose() {
    _volumeDebounce?.cancel();
    super.dispose();
  }

  void _pushVolume(double value) {
    setState(() {
      _volume = value;
      _muted = value <= 0;
    });

    // Sliders emit continuously; only the settled value is sent.
    _volumeDebounce?.cancel();
    _volumeDebounce = Timer(const Duration(milliseconds: 180), () {
      ref.read(remoteProvider)?.setVolume(value.round());
    });
  }

  /// Runs a command and surfaces any failure.
  ///
  /// A silent failure is the worst outcome for a remote control: the user
  /// would assume the key had worked.
  ///
  /// Success is confirmed too. That is deliberate - on a laggy link the user
  /// cannot otherwise tell a key that registered from one that was dropped, and
  /// without confirmation the natural response is to tap again, which is how a
  /// single shortcut ends up running three times.
  Future<void> _run(
    Future<Ack> Function() action, {
    String? success,
  }) async {
    if (_commandInProgress) return;
    setState(() => _commandInProgress = true);

    try {
      final ack = await action();
      if (!mounted) return;

      if (!ack.ok) {
        _toast(ack.error.isEmpty ? 'The PC did not accept it' : ack.error);
        return;
      }
      if (success != null) {
        _toast(success, duration: const Duration(milliseconds: 900));
      }
    } catch (error) {
      if (mounted) _toast('Command failed: $error');
    } finally {
      if (mounted) setState(() => _commandInProgress = false);
    }
  }

  void _toast(String message, {Duration duration = const Duration(seconds: 2)}) {
    final messenger = ScaffoldMessenger.of(context);
    messenger
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(message), duration: duration));
  }

  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);
    ref.listen(remoteLinkStateProvider, (previous, next) {
      if (next.valueOrNull != LinkState.ready) {
        _volumeDebounce?.cancel();
        _volumeDebounce = null;
      }
    });
    // Gates on a live socket, not merely a stored pairing: a pairing outlives
    // the connection, so the looser check left buttons looking live while every
    // tap was discarded.
    final enabled = (remote?.connected ?? false) && !_commandInProgress;
    // Non-null only when enabled. Every onPressed below is guarded by
    // `enabled`, so promoting once here removes the null check from each
    // closure without weakening type safety anywhere.
    final link = enabled ? remote : null;

    return SingleChildScrollView(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SectionTitle(label: 'Media keys'),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            child: Column(
              children: [
                Row(
                  mainAxisAlignment: MainAxisAlignment.spaceEvenly,
                  children: [
                    DpadButton(
                      label: 'Prev',
                      icon: Icons.skip_previous,
                      diameter: 60,
                      onPressed: enabled
                          ? () => _run(() => link!.tapKey('mediaprev'))
                          : null,
                    ),
                    DpadButton(
                      label: 'Play',
                      icon: Icons.play_arrow,
                      diameter: 72,
                      onPressed: enabled
                          ? () => _run(() => link!.tapKey('mediapause'))
                          : null,
                    ),
                    DpadButton(
                      label: 'Next',
                      icon: Icons.skip_next,
                      diameter: 60,
                      onPressed: enabled
                          ? () => _run(() => link!.tapKey('medianext'))
                          : null,
                    ),
                  ],
                ),
                const SizedBox(height: AppTokens.gap),
                Row(
                  children: [
                    Expanded(
                      child: RemoteButton(
                        label: 'Stop',
                        icon: Icons.stop,
                        onPressed: enabled
                            ? () => _run(() => link!.tapKey('mediastop'))
                            : null,
                      ),
                    ),
                    const SizedBox(width: AppTokens.gapSmall),
                    Expanded(
                      child: RemoteButton(
                        label: _muted ? 'Unmute' : 'Mute',
                        icon: _muted ? Icons.volume_off : Icons.volume_mute,
                        active: _muted,
                        onPressed: enabled
                            ? () => _run(() => link!.tapKey('mute'))
                            : null,
                      ),
                    ),
                  ],
                ),
              ],
            ),
          ),

          const SizedBox(height: 24),
          SectionTitle(label: 'Volume'),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            child: Row(
              children: [
                RemoteButton(
                  label: 'Down',
                  icon: Icons.volume_down,
                  compact: true,
                  onPressed: enabled
                      ? () => _run(() => link!.tapKey('voldown'))
                      : null,
                ),
                Expanded(
                  child: SliderTheme(
                    data: SliderThemeData(
                      activeTrackColor: AppColors.accent,
                      inactiveTrackColor: AppColors.glassBorder,
                      thumbColor: AppColors.accent,
                      overlayColor: AppColors.accent.withValues(alpha: 0.15),
                      trackHeight: 3,
                    ),
                    child: Slider(
                      value: _volume,
                      max: 100,
                      onChanged: enabled ? _pushVolume : null,
                    ),
                  ),
                ),
                RemoteButton(
                  label: 'Up',
                  icon: Icons.volume_up,
                  compact: true,
                  onPressed: enabled
                      ? () => _run(() => link!.tapKey('volup'))
                      : null,
                ),
                SizedBox(
                  width: 46,
                  child: Text(
                    '${_volume.round()}%',
                    textAlign: TextAlign.right,
                    style: monoStyle(13, FontWeight.w700,
                        color: AppColors.accent),
                  ),
                ),
              ],
            ),
          ),

          const SizedBox(height: 24),
          SectionTitle(label: 'Desktop shortcuts'),
          const SizedBox(height: AppTokens.gapSmall),
          // These send real key chords rather than shell macros.
          //
          // A macro shells out to explorer.exe or locks the workstation, which
          // needs matching binaries on the PC and fails silently when they are
          // absent. Win+E is the same action the user would press themselves,
          // works on any Windows install, and cannot get out of step with the
          // shell's own shortcut.
          GlassPanel(
            child: Wrap(
              spacing: AppTokens.gapSmall,
              runSpacing: AppTokens.gapSmall,
              children: [
                for (final s in _shortcutSpecs)
                  RemoteButton(
                    label: s.en,
                    icon: s.icon,
                    onPressed: enabled
                        ? () => _run(
                              () => link!.chord(s.mods, s.key),
                              success: s.en,
                            )
                        : null,
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
