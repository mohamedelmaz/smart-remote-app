import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/commands.dart';
import '../core/remote_link.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'pairing_screen.dart';

/// Which on-screen keyboard the user is typing with.
enum KeyboardMode {
  /// The app's own grid of keys.
  ///
  /// This is the default because the grid can send named keys, chords and
  /// modifier latches that a phone keyboard has no way to express.
  virtual,

  /// The phone's own system keyboard.
  native,
}

/// The on-screen keyboard: type text and send named keys to the PC.
///
/// Characters are sent with the `text.type` command rather than as individual
/// virtual key codes. That is deliberate: virtual key codes depend on the PC's
/// active keyboard layout, so typing "e" on an AZERTY PC would produce "r"
/// unless the app knew the layout. Unicode text injection is layout
/// independent, which is why the label the user sees is always what the PC
/// receives.
class KeyboardScreen extends ConsumerStatefulWidget {
  const KeyboardScreen({super.key});

  @override
  ConsumerState<KeyboardScreen> createState() => _KeyboardScreenState();
}

class _KeyboardScreenState extends ConsumerState<KeyboardScreen>
    with WidgetsBindingObserver {
  /// Modifier keys currently held down on the PC.
  ///
  /// The PC cannot report which modifiers are latched, so this mirrors what
  /// the app has sent. Latched modifiers are released on dispose, otherwise
  /// an interrupted "Shift" would leave the PC in a stuck state.
  final Set<String> _latched = {};

  /// The pending modifier prefix applied to the next character.
  ///
  /// Held with a visible active state, this turns "Ctrl then C" into a single
  /// chord rather than requiring both keys to be pressed at once.
  final List<String> _pendingMods = [];

  /// Which keyboard is active. Defaults to the app's own grid.
  KeyboardMode _mode = KeyboardMode.virtual;

  /// Drives focus for the native keyboard's text field.
  ///
  /// A FocusNode is required rather than a GlobalKey: showing the system
  /// keyboard means asking this node for focus, and hiding it means unfocusing
  /// the same node. Doing it by node also means the toggle works after a
  /// rebuild, which a raw requestFocus on a rebuilt context does not.
  final FocusNode _nativeFocus = FocusNode();

  /// The controller backing the native keyboard field.
  final TextEditingController _nativeController = TextEditingController();

  /// The text already delivered to the PC.
  ///
  /// Native keyboards do not report keystrokes, only the whole edited string,
  /// so the app diffs against the last value it sent and transmits only the
  /// delta. Without this, every autocorrect or swipe-typing correction would
  /// re-send the entire field and garble the PC's input.
  String _sentNativeText = '';

  /// The link captured while mounted, for use from [dispose].
  ///
  /// Riverpod throws if `ref` is read once the element is disposed, and the
  /// release of latched modifiers on the way out is exactly the case where that
  /// matters: a stuck Ctrl would turn the user's next click into a shortcut.
  RemoteLink? _capturedLink;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _capturedLink = ref.read(remoteLinkProvider);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // A modifier latched on the PC survives the app being backgrounded, and
    // the user will not remember it was held when they come back. The next
    // click then becomes a shortcut they never asked for, so latches are
    // dropped when the app leaves the foreground.
    if (state == AppLifecycleState.resumed || _latched.isEmpty) return;
    final remote = ref.read(remoteProvider);
    for (final mod in _latched) {
      remote?.latch(mod, false);
    }
    _latched.clear();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _nativeFocus.dispose();
    _nativeController.dispose();
    // Best-effort release; a failure here means the socket is already gone,
    // and the server drops latches on disconnect anyway.
    for (final mod in _latched) {
      // `ref` is unusable from dispose, so the link is read from a value
      // captured while mounted. Failure here only means the socket is already
      // gone, and the server drops latches on disconnect anyway.
      final link = _capturedLink;
      if (link != null) Remote(link).latch(mod, false);
    }
    super.dispose();
  }

  /// Switches between the app's grid and the phone's own keyboard.
  ///
  /// Focus is requested immediately when switching to native, because the system
  /// keyboard only appears for a focused field: deferring it to the next tap
  /// would leave the user staring at a text field with no keyboard, which looks
  /// broken. Switching back unfocuses for the same reason in reverse.
  void _setMode(KeyboardMode mode) {
    if (_mode == mode) return;
    setState(() => _mode = mode);

    if (mode == KeyboardMode.native) {
      // Reopening the keyboard with existing text would otherwise re-send it.
      _sentNativeText = _nativeController.text;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (!mounted) return;
        _nativeFocus.requestFocus();
      });
    } else {
      _nativeFocus.unfocus();
    }
  }

  /// Sends only what the user actually added to the native field.
  ///
  /// A deletion is sent as a Backspace so the PC's cursor moves back, which is
  /// what makes the native keyboard genuinely usable for editing rather than
  /// append-only.
  void _onNativeChanged(String value) {
    final remote = ref.read(remoteProvider);
    if (remote == null) return;

    final previous = _sentNativeText;
    if (value.startsWith(previous)) {
      final added = value.substring(previous.length);
      if (added.isNotEmpty) {
        _sentNativeText = value;
        _report(() => remote.typeText(added));
      }
      return;
    }

    // The field was edited in the middle, which no keystroke-by-keystroke
    // scheme can represent. Fall back to rewinding and retyping, which is
    // slow but correct.
    final backspaces = previous.length - _commonPrefixLength(previous, value);
    _sentNativeText = value;
    _report(() async {
      for (var i = 0; i < backspaces; i++) {
        await remote.tapKey('backspace');
      }
      return value.isEmpty ? const Ack(ok: true) : remote.typeText(value);
    });
  }

  /// Length of the shared prefix of [a] and [b].
  int _commonPrefixLength(String a, String b) {
    final limit = a.length < b.length ? a.length : b.length;
    var i = 0;
    while (i < limit && a.codeUnitAt(i) == b.codeUnitAt(i)) {
      i++;
    }
    return i;
  }

  /// Runs an action and surfaces any failure to the user.
  ///
  /// A silent failure is the worst outcome for a remote control: the user
  /// would assume the key had worked on the PC.
  Future<void> _report(Future<Ack> Function() action) async {
    final ack = await action();
    if (!ack.ok && ack.error.isNotEmpty && mounted) {
      ScaffoldMessenger.of(context)
          .showSnackBar(SnackBar(content: Text(ack.error)));
    }
  }

  /// Types a character, folding in any pending modifiers.
  Future<void> _typeCharacter(_KeySpec spec) async {
    final remote = ref.read(remoteProvider);
    if (remote == null) return;

    // Digits ignore modifiers: "Ctrl+1" is not a meaningful chord here, and
    // sending it would surprise a user who only wanted the digit.
    if (_pendingMods.isEmpty || spec.digit) {
      await _report(() => remote.typeText(spec.label));
      return;
    }

    final mods = List<String>.from(_pendingMods);
    await _report(() => remote.chord(mods, spec.key));
    if (mounted) setState(_pendingMods.clear);
  }

  /// Taps a named key such as Enter or Escape.
  Future<void> _tapKey(String key) async {
    final remote = ref.read(remoteProvider);
    if (remote == null) return;
    await _report(() => remote.tapKey(key));
  }

  /// Stages or clears a modifier chord prefix.
  void _togglePendingMod(String mod) {
    setState(() {
      if (_pendingMods.contains(mod)) {
        _pendingMods.remove(mod);
      } else if (_pendingMods.length < 2) {
        // Two modifiers is the practical limit for this UI (Ctrl+Alt); more
        // would wrap unpredictably in the narrow grid.
        _pendingMods.add(mod);
      }
    });
  }

  /// Latches a modifier down or up on the PC.
  Future<void> _toggleLatch(String mod) async {
    final remote = ref.read(remoteProvider);
    if (remote == null) return;

    final nowLatched = !_latched.contains(mod);
    final ack = await remote.latch(mod, nowLatched);
    if (!ack.ok) {
      // The PC rejected it, so the local mirror must not drift from reality.
      if (ack.error.isNotEmpty && mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(ack.error)));
      }
      return;
    }
    if (!mounted) return;
    setState(() {
      if (nowLatched) {
        _latched.add(mod);
      } else {
        _latched.remove(mod);
      }
    });
  }

  /// Routes a special key to either a latch or a plain tap.
  void _onSpecialPressed(_SpecialKey key) {
    switch (key.key) {
      case 'shift':
      case 'ctrl':
      case 'alt':
        _toggleLatch(key.key);
      default:
        _tapKey(key.key);
    }
  }

  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);
    final enabled = remote != null;

    return SingleChildScrollView(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const SectionTitle(label: 'Text'),
          const SizedBox(height: AppTokens.gapSmall),
          _ModeSwitcher(mode: _mode, onChanged: _setMode),
          const SizedBox(height: AppTokens.gapSmall),
          // The native field appears *above* the grid rather than replacing it.
          // The grid's named keys (Enter, Backspace, arrows) and its modifier
          // staging stay useful while typing on the phone, so hiding them would
          // remove capability rather than resolve a conflict. AnimatedSize
          // keeps the switch from snapping, which reads as a glitch.
          AnimatedSize(
            duration: AppTokens.transition,
            curve: AppTokens.curve,
            alignment: Alignment.topCenter,
            child: _mode == KeyboardMode.native
                ? _NativeInput(
                    controller: _nativeController,
                    focusNode: _nativeFocus,
                    enabled: enabled,
                    onChanged: _onNativeChanged,
                    onSend: () => _tapKey('enter'),
                  )
                : const SizedBox(width: double.infinity),
          ),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            padding: const EdgeInsets.all(AppTokens.gapSmall),
            child: Row(
              children: [
                Expanded(
                  child: RemoteButton(
                    label: 'Paste',
                    icon: Icons.content_paste,
                    compact: true,
                    expand: true,
                    onPressed: enabled
                        ? () => _report(() => remote.chord(['ctrl'], 'v'))
                        : null,
                  ),
                ),
                const SizedBox(width: AppTokens.gapSmall),
                Expanded(
                  child: RemoteButton(
                    label: 'Copy',
                    icon: Icons.content_copy,
                    compact: true,
                    expand: true,
                    onPressed: enabled
                        ? () => _report(() => remote.chord(['ctrl'], 'c'))
                        : null,
                  ),
                ),
                const SizedBox(width: AppTokens.gapSmall),
                Expanded(
                  child: RemoteButton(
                    label: 'All',
                    icon: Icons.select_all,
                    compact: true,
                    expand: true,
                    onPressed: enabled
                        ? () => _report(() => remote.chord(['ctrl'], 'a'))
                        : null,
                  ),
                ),
              ],
            ),
          ),

          const SizedBox(height: 20),
          SectionTitle(
            label: 'Special keys',
            // Latched modifiers are spelled out, so the user can see what is
            // currently held down on the PC.
            trailing: _latched.isEmpty
                ? null
                : Text(
                    'Held: ${_latched.join(', ')}',
                    style: monoStyle(
                      11,
                      FontWeight.w700,
                      color: AppColors.warning,
                    ),
                  ),
          ),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            padding: const EdgeInsets.all(AppTokens.gapSmall),
            child: Wrap(
              spacing: AppTokens.gapSmall,
              runSpacing: AppTokens.gapSmall,
              children: [
                for (final key in _kSpecialKeys)
                  RemoteButton(
                    label: key.label,
                    icon: key.icon,
                    compact: true,
                    onPressed: enabled ? () => _onSpecialPressed(key) : null,
                    active: _latched.contains(key.key),
                  ),
              ],
            ),
          ),

          const SizedBox(height: 20),
          const SectionTitle(label: 'Navigation'),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            padding: const EdgeInsets.all(AppTokens.gapSmall),
            child: Row(
              mainAxisAlignment: MainAxisAlignment.spaceEvenly,
              children: [
                for (final key in _kNavKeys)
                  DpadButton(
                    label: key.label,
                    icon: key.icon,
                    diameter: 56,
                    onPressed: enabled
                        ? () => _report(() => remote.tapKey(key.key))
                        : null,
                  ),
              ],
            ),
          ),
          const SizedBox(height: 20),
          SectionTitle(
            label: 'Keyboard',
            // A staged chord is shown here, so a pending modifier is never
            // hidden state the user has to guess about.
            trailing: _pendingMods.isEmpty
                ? null
                : Text(
                    'Next: ${_pendingMods.join(' + ')}',
                    style: monoStyle(
                      11,
                      FontWeight.w700,
                      color: AppColors.accent,
                    ),
                  ),
          ),
          const SizedBox(height: AppTokens.gapSmall),
          GlassPanel(
            padding: const EdgeInsets.all(AppTokens.gapSmall),
            child: Column(
              children: [
                for (var i = 0; i < _kKeyboardRows.length; i++) ...[
                  if (i > 0) const SizedBox(height: 6),
                  _KeyRow(
                    keys: _kKeyboardRows[i],
                    enabled: enabled,
                    onKey: _typeCharacter,
                  ),
                ],
                const SizedBox(height: 6),
                // The modifier prefix row sits directly above the space bar,
                // where a thumb naturally rests.
                Row(
                  children: [
                    Expanded(
                      child: RemoteButton(
                        label: 'Ctrl',
                        compact: true,
                        expand: true,
                        active: _pendingMods.contains('ctrl'),
                        onPressed: enabled
                            ? () => _togglePendingMod('ctrl')
                            : null,
                      ),
                    ),
                    const SizedBox(width: AppTokens.gapSmall),
                    Expanded(
                      child: RemoteButton(
                        label: 'Alt',
                        compact: true,
                        expand: true,
                        active: _pendingMods.contains('alt'),
                        onPressed: enabled
                            ? () => _togglePendingMod('alt')
                            : null,
                      ),
                    ),
                    const SizedBox(width: AppTokens.gapSmall),
                    Expanded(
                      flex: 3,
                      child: RemoteButton(
                        label: 'Space',
                        compact: true,
                        expand: true,
                        onPressed: enabled
                            ? () => _report(() => remote.typeText(' '))
                            : null,
                      ),
                    ),
                    const SizedBox(width: AppTokens.gapSmall),
                    Expanded(
                      child: RemoteButton(
                        label: 'Clear',
                        compact: true,
                        expand: true,
                        // Clears the staged chord, which is easy to forget and
                        // would silently alter the next key pressed.
                        onPressed: _pendingMods.isEmpty
                            ? null
                            : () => setState(_pendingMods.clear),
                      ),
                    ),
                  ],
                ),
              ],
            ),
          ),
          const SizedBox(height: AppTokens.gap),
        ],
      ),
    );
  }
}

/// Rows of the on-screen keyboard.
///
/// [label] is the text shown on the key and typed into the PC; [key] is the
/// protocol name the server resolves to a virtual key code. Keeping them
/// adjacent makes a mismatch obvious, because the user only ever sees the
/// label and has no way to know the underlying name.
const List<List<_KeySpec>> _kKeyboardRows = [
  // Number row.
  [
    _KeySpec('1', '1', digit: true),
    _KeySpec('2', '2', digit: true),
    _KeySpec('3', '3', digit: true),
    _KeySpec('4', '4', digit: true),
    _KeySpec('5', '5', digit: true),
    _KeySpec('6', '6', digit: true),
    _KeySpec('7', '7', digit: true),
    _KeySpec('8', '8', digit: true),
    _KeySpec('9', '9', digit: true),
    _KeySpec('0', '0', digit: true),
  ],
  // Top letter row.
  [
    _KeySpec('Q', 'q'),
    _KeySpec('W', 'w'),
    _KeySpec('E', 'e'),
    _KeySpec('R', 'r'),
    _KeySpec('T', 't'),
    _KeySpec('Y', 'y'),
    _KeySpec('U', 'u'),
    _KeySpec('I', 'i'),
    _KeySpec('O', 'o'),
    _KeySpec('P', 'p'),
  ],
  // Home row.
  [
    _KeySpec('A', 'a'),
    _KeySpec('S', 's'),
    _KeySpec('D', 'd'),
    _KeySpec('F', 'f'),
    _KeySpec('G', 'g'),
    _KeySpec('H', 'h'),
    _KeySpec('J', 'j'),
    _KeySpec('K', 'k'),
    _KeySpec('L', 'l'),
  ],
  // Bottom letter row.
  [
    _KeySpec('Z', 'z'),
    _KeySpec('X', 'x'),
    _KeySpec('C', 'c'),
    _KeySpec('V', 'v'),
    _KeySpec('B', 'b'),
    _KeySpec('N', 'n'),
    _KeySpec('M', 'm'),
  ],
];

/// One character key on the on-screen keyboard.
class _KeySpec {
  const _KeySpec(this.label, this.key, {this.digit = false});

  /// The character shown on the key and typed into the PC.
  final String label;

  /// The protocol key name sent to the server for a chord.
  final String key;

  /// Digits are sent as text rather than as a chord, so a staged modifier does
  /// not turn "5" into an unintended Ctrl+5.
  final bool digit;
}

/// The non-character keys shown above the letter rows.
const List<_SpecialKey> _kSpecialKeys = [
  _SpecialKey('Tab', 'tab', Icons.arrow_forward),
  _SpecialKey('Caps', 'capslock', Icons.keyboard_capslock),
  _SpecialKey('Enter', 'enter', Icons.keyboard_return),
  _SpecialKey('Shift', 'shift', Icons.arrow_upward),
  _SpecialKey('Ctrl', 'ctrl', Icons.keyboard_control_key),
  _SpecialKey('Alt', 'alt', Icons.keyboard_command_key),
  _SpecialKey('Esc', 'escape', Icons.close),
  _SpecialKey('Bksp', 'backspace', Icons.backspace_outlined),
];

/// A named key that is not a character.
class _SpecialKey {
  const _SpecialKey(this.label, this.key, this.icon);

  final String label;
  final String key;
  final IconData icon;
}

/// The cursor-navigation keys.
const List<_NavKey> _kNavKeys = [
  _NavKey('Up', 'up', Icons.keyboard_arrow_up),
  _NavKey('Down', 'down', Icons.keyboard_arrow_down),
  _NavKey('Left', 'left', Icons.keyboard_arrow_left),
  _NavKey('Right', 'right', Icons.keyboard_arrow_right),
];

/// A cursor-navigation key.
class _NavKey {
  const _NavKey(this.label, this.key, this.icon);

  final String label;
  final String key;
  final IconData icon;
}

/// One row of character keys.
class _KeyRow extends StatelessWidget {
  const _KeyRow({
    required this.keys,
    required this.enabled,
    required this.onKey,
  });

  final List<_KeySpec> keys;
  final bool enabled;
  final ValueChanged<_KeySpec> onKey;

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        for (final key in keys)
          Expanded(
            child: Padding(
              padding: const EdgeInsets.symmetric(horizontal: 2),
              child: _KeyCap(
                spec: key,
                onPressed: enabled ? () => onKey(key) : null,
              ),
            ),
          ),
      ],
    );
  }
}

/// A single character key cap.
class _KeyCap extends StatefulWidget {
  const _KeyCap({required this.spec, required this.onPressed});

  final _KeySpec spec;
  final VoidCallback? onPressed;

  @override
  State<_KeyCap> createState() => _KeyCapState();
}

class _KeyCapState extends State<_KeyCap> {
  /// True while held, giving per-key visual feedback.
  ///
  /// A keyboard must acknowledge every press: the PC is out of sight, so the
  /// press highlight is the user's only confirmation that a key registered.
  bool _pressed = false;

  @override
  Widget build(BuildContext context) {
    final enabled = widget.onPressed != null;

    final cap = AnimatedContainer(
      duration: AppTokens.transition,
      curve: AppTokens.curve,
      height: 46,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: _pressed
            ? AppColors.accent.withValues(alpha: 0.28)
            : AppColors.glassStrong,
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        border: Border.all(
          color: _pressed ? AppColors.accent : AppColors.glassBorder,
        ),
        boxShadow: _pressed ? AppTokens.glowSubtle() : null,
      ),
      child: Text(widget.spec.label, style: monoStyle(15, FontWeight.w600)),
    );

    if (!enabled) return Opacity(opacity: 0.5, child: cap);

    return GestureDetector(
      behavior: HitTestBehavior.opaque,
      onTapDown: (_) => setState(() => _pressed = true),
      onTapUp: (_) => setState(() => _pressed = false),
      onTapCancel: () => setState(() => _pressed = false),
      onTap: () {
        // A light tick matches the feel of a physical key. On a remote the user
        // is looking at the phone, not the PC, so touch feedback matters more
        // here than it would for an on-device keyboard.
        HapticFeedback.selectionClick();
        widget.onPressed!();
      },
      child: cap,
    );
  }
}

/// The Native / Virtual keyboard toggle.
///
/// A segmented control rather than a single icon button, because the control's
/// whole job is to report which mode is active. An icon that changes shape
/// forces the user to remember what the last shape meant; two labelled halves
/// state the current mode outright, which is what makes it trustworthy enough
/// to rely on for something that silently changes how typing behaves.
class _ModeSwitcher extends StatelessWidget {
  const _ModeSwitcher({required this.mode, required this.onChanged});

  final KeyboardMode mode;
  final ValueChanged<KeyboardMode> onChanged;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      padding: const EdgeInsets.all(4),
      child: Row(
        children: [
          _ModeSegment(
            label: 'App keyboard',
            icon: Icons.keyboard_alt_outlined,
            selected: mode == KeyboardMode.virtual,
            onTap: () => onChanged(KeyboardMode.virtual),
          ),
          _ModeSegment(
            label: 'Phone keyboard',
            icon: Icons.smartphone,
            selected: mode == KeyboardMode.native,
            onTap: () => onChanged(KeyboardMode.native),
          ),
        ],
      ),
    );
  }
}

/// One half of the mode toggle.
class _ModeSegment extends StatelessWidget {
  const _ModeSegment({
    required this.label,
    required this.icon,
    required this.selected,
    required this.onTap,
  });

  final String label;
  final IconData icon;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final color = selected ? AppColors.accent : AppColors.textSecondary;

    return Expanded(
      child: Semantics(
        button: true,
        selected: selected,
        label: label,
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: onTap,
          child: AnimatedContainer(
            duration: AppTokens.transition,
            curve: AppTokens.curve,
            padding: const EdgeInsets.symmetric(vertical: 12),
            decoration: BoxDecoration(
              color: selected
                  ? AppColors.accent.withValues(alpha: 0.18)
                  : Colors.transparent,
              borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
              border: Border.all(
                color: selected ? AppColors.accent : Colors.transparent,
              ),
              boxShadow: selected ? AppTokens.glowSubtle() : null,
            ),
            child: Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                Icon(icon, size: 16, color: color),
                const SizedBox(width: 6),
                Flexible(
                  child: Text(
                    label,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: interStyle(12, FontWeight.w600, color: color),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// The text field that hosts the phone's own keyboard.
///
/// It is a real [TextField] rather than a custom key handler because the system
/// keyboard only attaches to a genuine input field. The field doubles as a
/// visible echo of what has been sent, which matters because the PC is out of
/// sight: without it the user has no confirmation that their typing arrived.
class _NativeInput extends StatelessWidget {
  const _NativeInput({
    required this.controller,
    required this.focusNode,
    required this.enabled,
    required this.onChanged,
    required this.onSend,
  });

  final TextEditingController controller;
  final FocusNode focusNode;
  final bool enabled;
  final ValueChanged<String> onChanged;
  final VoidCallback onSend;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      padding: const EdgeInsets.all(AppTokens.gapSmall),
      // TextField needs a Material ancestor for its ink splash and selection
      // painting, and this app's panels deliberately are not Material. Without
      // this the widget throws an assertion the moment the phone keyboard is
      // selected, so the toggle would crash rather than switch.
      child: Material(
        type: MaterialType.transparency,
        child: Row(
          children: [
            Expanded(
              child: TextField(
                controller: controller,
                focusNode: focusNode,
                enabled: enabled,
                onChanged: onChanged,
                onSubmitted: (_) => onSend(),
                // Single line keeps the system keyboard compact; this is a remote
                // control, not a document editor.
                maxLines: 1,
                textInputAction: TextInputAction.send,
                autocorrect: true,
                enableSuggestions: true,
                style: interStyle(14, FontWeight.w600),
                cursorColor: AppColors.accent,
                cursorRadius: const Radius.circular(1),
                decoration: InputDecoration(
                  isDense: true,
                  hintText: 'Type with the phone keyboard',
                  hintStyle: interStyle(
                    13,
                    FontWeight.w400,
                    color: AppColors.textSecondary,
                  ),
                  prefixIcon: const Icon(
                    Icons.keyboard,
                    size: 18,
                    color: AppColors.accent,
                  ),
                  border: InputBorder.none,
                ),
              ),
            ),
            const SizedBox(width: AppTokens.gapSmall),
            RemoteButton(
              label: 'Enter',
              icon: Icons.keyboard_return,
              compact: true,
              onPressed: enabled ? onSend : null,
            ),
          ],
        ),
      ),
    );
  }
}
