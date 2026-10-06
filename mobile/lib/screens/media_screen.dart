import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/commands.dart';
import '../core/remote_link.dart';
import '../l10n/strings.dart';
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
    required this.ar,
    required this.icon,
    required this.mods,
    required this.key,
  });

  final String en;
  final String ar;
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
    ar: 'إظهار سطح المكتب',
    icon: Icons.desktop_windows,
    mods: ['win'],
    key: 'd',
  ),
  _Shortcut(
    en: 'File Explorer',
    ar: 'مستكشف الملفات',
    icon: Icons.folder_open,
    mods: ['win'],
    key: 'e',
  ),
  _Shortcut(
    en: 'Lock PC',
    ar: 'قفل الجهاز',
    icon: Icons.lock,
    mods: ['win'],
    key: 'l',
  ),
  _Shortcut(
    en: 'Switch Apps',
    ar: 'تبديل التطبيقات',
    icon: Icons.swap_horiz,
    mods: ['alt'],
    key: 'tab',
  ),
  _Shortcut(
    en: 'Task Manager',
    ar: 'مدير المهام',
    icon: Icons.speed,
    mods: ['ctrl', 'shift'],
    key: 'esc',
  ),
  _Shortcut(
    en: 'Snipping Tool',
    ar: 'أداة القص',
    icon: Icons.crop_free,
    mods: ['win', 'shift'],
    key: 's',
  ),
  _Shortcut(
    en: 'Copy',
    ar: 'نسخ',
    icon: Icons.content_copy,
    mods: ['ctrl'],
    key: 'c',
  ),
  _Shortcut(
    en: 'Paste',
    ar: 'لصق',
    icon: Icons.content_paste,
    mods: ['ctrl'],
    key: 'v',
  ),
  _Shortcut(
    en: 'Cut',
    ar: 'قص',
    icon: Icons.content_cut,
    mods: ['ctrl'],
    key: 'x',
  ),
  _Shortcut(
    en: 'Undo',
    ar: 'تراجع',
    icon: Icons.undo,
    mods: ['ctrl'],
    key: 'z',
  ),
  _Shortcut(
    en: 'Redo',
    ar: 'إعادة',
    icon: Icons.redo,
    mods: ['ctrl'],
    key: 'y',
  ),
  _Shortcut(
    en: 'Find',
    ar: 'بحث',
    icon: Icons.search,
    mods: ['ctrl'],
    key: 'f',
  ),
  _Shortcut(
    en: 'Refresh',
    ar: 'تحديث',
    icon: Icons.refresh,
    // F5 needs no modifier. It is sent as a chord with an empty modifier list
    // rather than as a tap so every shortcut goes through one code path - and
    // Chord already handles that case, sending just the key down and up.
    mods: [],
    key: 'f5',
  ),
  _Shortcut(
    en: 'Settings',
    ar: 'الإعدادات',
    icon: Icons.settings,
    mods: ['win'],
    key: 'i',
  ),
  _Shortcut(
    en: 'Run',
    ar: 'تشغيل',
    icon: Icons.terminal,
    mods: ['win'],
    key: 'r',
  ),
  _Shortcut(
    en: 'Quick Assist',
    ar: 'المساعدة السريعة',
    icon: Icons.support_agent,
    mods: ['win'],
    key: 'q',
  ),
  _Shortcut(
    en: 'Project',
    ar: 'المشروع',
    icon: Icons.video_camera_front,
    mods: ['ctrl'],
    key: 'p',
  ),
  _Shortcut(
    en: 'Close Window',
    ar: 'إغلاق النافذة',
    icon: Icons.close,
    mods: ['alt'],
    key: 'f4',
  ),
  _Shortcut(
    en: 'Rename',
    ar: 'إعادة تسمية',
    icon: Icons.drive_file_rename_outline,
    mods: [],
    key: 'f2',
  ),
  _Shortcut(
    en: 'Save',
    ar: 'حفظ',
    icon: Icons.save,
    mods: ['ctrl'],
    key: 's',
  ),
  _Shortcut(
    en: 'Search',
    ar: 'بحث',
    icon: Icons.search,
    mods: ['win'],
    key: 's',
  ),
  _Shortcut(
    en: 'Action Center',
    ar: 'مركز الإشعارات',
    icon: Icons.notifications,
    mods: ['win'],
    key: 'a',
  ),
  _Shortcut(
    en: 'Active Apps',
    ar: 'التطبيقات المفتوحة',
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
    // Watching the language here is what makes a language change repaint this
    // screen. A cached field would be initialised once and keep showing the
    // previous language until the app was restarted.
    final t = T(ref.watch(langProvider));

    return SingleChildScrollView(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SectionTitle(label: t.mediaKeys),
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
          SectionTitle(label: t.volume),
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
          SectionTitle(label: t.desktopShortcuts),
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
                    label: t.text(s.en, s.ar),
                    icon: s.icon,
                    onPressed: enabled
                        ? () => _run(
                              () => link!.chord(s.mods, s.key),
                              success: t.text(s.en, s.ar),
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
