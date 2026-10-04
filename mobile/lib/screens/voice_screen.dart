import 'dart:async';
import 'dart:typed_data';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:permission_handler/permission_handler.dart';
import 'package:record/record.dart';

import '../core/commands.dart';
import '../core/remote_link.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'pairing_screen.dart';

/// The sample rate the PC opens its audio sink with.
///
/// These values must match the server's VoiceSampleRate / VoiceChannels /
/// VoiceBits exactly. waveOut does not resample, so sending audio at the
/// phone's default 44.1 kHz stereo would play back as noise at the wrong speed.
const int kVoiceSampleRate = 16000;
const int kVoiceChannels = 1;

/// Streams the phone's microphone to the PC's speakers.
///
/// The phone acts as a walkie-talkie: tap to talk and the PC plays it. Audio
/// is captured as raw PCM and sent over the same WebSocket as the commands,
/// framed by a leading marker byte, so no second connection is needed.
class VoiceScreen extends ConsumerStatefulWidget {
  const VoiceScreen({super.key});

  @override
  ConsumerState<VoiceScreen> createState() => _VoiceScreenState();
}

class _VoiceScreenState extends ConsumerState<VoiceScreen> {
  final AudioRecorder _recorder = AudioRecorder();

  /// True while the microphone is being captured and streamed.
  bool _streaming = false;

  /// True while a start or stop is in flight, which blocks double taps.
  bool _busy = false;

  /// The last error, shown instead of a bare button when something failed.
  String? _error;

  /// Bytes streamed in the current session, used as an activity signal.
  int _bytesSent = 0;

  StreamSubscription<Uint8List>? _audioSub;

  /// The link captured while mounted, for use from [dispose].
  ///
  /// Riverpod throws if `ref` is read after the element is disposed. Stopping
  /// the PC-side sink on the way out is exactly the case that needs it: without
  /// it the PC keeps a waveOut device open after the screen is gone, and the
  /// app crashes on every tab switch with an unhelpful ref error.
  RemoteLink? _capturedLink;

  @override
  void initState() {
    super.initState();
    _capturedLink = ref.read(remoteLinkProvider);
  }

  @override
  void dispose() {
    _audioSub?.cancel();
    // The PC-side sink is stopped best-effort so it does not keep a waveOut
    // device open after this screen is gone.
    _recorder.dispose();
    final link = _capturedLink;
    if (link != null) Remote(link).stopAudio();
    super.dispose();
  }

  void _fail(String message) {
    if (!mounted) return;
    setState(() {
      _streaming = false;
      _busy = false;
      _error = message;
    });
  }

  /// Starts capture and streams it to the PC.
  Future<void> _start() async {
    if (_busy || _streaming) return;
    setState(() {
      _busy = true;
      _error = null;
      _bytesSent = 0;
    });

    try {
      // Permission is requested here rather than at launch, so the user sees
      // why it is needed at the moment they press the button.
      var granted = await _recorder.hasPermission();
      if (!granted) {
        granted = await Permission.microphone.request().isGranted;
      }
      if (!granted) {
        throw const VoiceException(
          'Microphone access is required to stream voice to the PC. '
          'Enable it in the phone settings.',
        );
      }

      final remote = ref.read(remoteProvider);
      if (remote == null) throw const VoiceException('Not connected to a PC.');

      // The PC must open its output device before audio arrives, or the first
      // chunks are dropped while waveOut starts up.
      final ack = await remote.startAudio();
      if (!ack.ok) {
        throw VoiceException(
          ack.error.isEmpty
              ? 'The PC could not open an audio output device.'
              : ack.error,
        );
      }

      final stream = await _recorder.startStream(
        const RecordConfig(
          encoder: AudioEncoder.pcm16bits,
          sampleRate: kVoiceSampleRate,
          numChannels: kVoiceChannels,
          // Gain control and noise suppression help on a desk microphone but
          // distort speech on a phone held at arm's length, so both stay off.
          autoGain: false,
          echoCancel: false,
          noiseSuppress: false,
        ),
      );

      _audioSub = stream.listen(
        (chunk) {
          remote.streamAudio(chunk);
          // A byte counter is the only honest "is it working" signal: the PC
          // sends no playback feedback, so a silent stream and a broken one
          // would otherwise look identical.
          if (mounted) setState(() => _bytesSent += chunk.length);
        },
        onError: (Object error) => _fail('Microphone error: $error'),
      );

      if (mounted) {
        setState(() {
          _streaming = true;
          _busy = false;
        });
      }
    } on VoiceException catch (error) {
      _fail(error.message);
    } catch (error) {
      _fail('Could not start voice: $error');
    }
  }

  /// Stops capture and tells the PC to close its output device.
  Future<void> _stop() async {
    if (_busy || !_streaming) return;
    setState(() => _busy = true);

    await _audioSub?.cancel();
    _audioSub = null;

    // A stop failure is not fatal: the local stream is already detached and
    // dispose() releases the recorder.
    try {
      await _recorder.stop();
    } catch (_) {
      // Intentionally ignored, see above.
    }

    await ref.read(remoteProvider)?.stopAudio();

    if (!mounted) return;
    setState(() {
      _streaming = false;
      _busy = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);

    return Padding(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        children: [
          const Spacer(),
          GlassPanel(
            padding: const EdgeInsets.all(24),
            child: Column(
              children: [
                _LevelIndicator(active: _streaming, bytes: _bytesSent),
                const SizedBox(height: AppTokens.gap),
                Text(
                  _streaming ? 'Streaming to PC' : 'Voice output',
                  style: interStyle(18, FontWeight.w700),
                ),
                const SizedBox(height: AppTokens.gapSmall),
                HintText(
                  _streaming
                      ? 'Speak now. Your PC is playing this audio.'
                      : 'Tap to stream your phone microphone to the PC '
                          'speakers.',
                ),
              ],
            ),
          ),
          const SizedBox(height: AppTokens.gap),
          if (_error != null) ...[
            _ErrorPanel(message: _error!),
            const SizedBox(height: AppTokens.gap),
          ],
          _TalkButton(
            streaming: _streaming,
            busy: _busy,
            enabled: remote != null,
            onStart: _start,
            onStop: _stop,
          ),
          const SizedBox(height: AppTokens.gap),
          // Stated in advance because it affects how the user holds the phone:
          // the audio plays as it arrives rather than after a delay.
          const HintText(
            'Audio is sent uncompressed at 16 kHz mono and plays on the PC as '
            'it arrives, so keep this screen open while talking.',
          ),
          const Spacer(),
        ],
      ),
    );
  }
}

/// A failure carrying a message meant for the user.
class VoiceException implements Exception {
  const VoiceException(this.message);

  final String message;

  @override
  String toString() => message;
}

/// The animated microphone indicator.
class _LevelIndicator extends StatelessWidget {
  const _LevelIndicator({required this.active, required this.bytes});

  final bool active;
  final int bytes;

  @override
  Widget build(BuildContext context) {
    // The ring pulses while streaming. A static icon would not distinguish
    // "connected and sending" from "connected but silent".
    return SizedBox(
      width: 96,
      height: 96,
      child: Stack(
        alignment: Alignment.center,
        children: [
          if (active)
            const _PulseRing()
          else
            Container(
              width: 88,
              height: 88,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                border: Border.all(color: AppColors.glassBorder, width: 2),
              ),
            ),
          Icon(
            active ? Icons.graphic_eq : Icons.mic_none,
            size: 40,
            color: active ? AppColors.accent : AppColors.textSecondary,
          ),
        ],
      ),
    );
  }
}

/// An expanding ring shown while audio is being streamed.
class _PulseRing extends StatefulWidget {
  const _PulseRing();

  @override
  State<_PulseRing> createState() => _PulseRingState();
}

class _PulseRingState extends State<_PulseRing>
    with SingleTickerProviderStateMixin {
  late final AnimationController _controller = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1400),
  )..repeat();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: _controller,
      builder: (context, _) {
        // The ring grows from the icon and fades out, so repeated pulses read
        // as a continuous activity signal rather than one flash.
        final t = _controller.value;
        return Container(
          width: 88 + 16 * t,
          height: 88 + 16 * t,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            border: Border.all(
              color: AppColors.accent.withValues(alpha: (1 - t) * 0.7),
              width: 2,
            ),
          ),
        );
      },
    );
  }
}

/// The large tap-to-talk button.
class _TalkButton extends StatelessWidget {
  const _TalkButton({
    required this.streaming,
    required this.busy,
    required this.enabled,
    required this.onStart,
    required this.onStop,
  });

  final bool streaming;
  final bool busy;
  final bool enabled;
  final VoidCallback onStart;
  final VoidCallback onStop;

  @override
  Widget build(BuildContext context) {
    final color = streaming ? AppColors.error : AppColors.accent;

    final button = AnimatedContainer(
      duration: AppTokens.transition,
      width: 150,
      height: 150,
      decoration: BoxDecoration(
        shape: BoxShape.circle,
        color: color.withValues(alpha: streaming ? 0.22 : 0.18),
        border: Border.all(color: color, width: 3),
        boxShadow: streaming
            ? [BoxShadow(color: color.withValues(alpha: 0.35), blurRadius: 24)]
            : AppTokens.glow(opacity: 0.3, blur: 26),
      ),
      child: Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(streaming ? Icons.stop : Icons.mic, size: 44, color: color),
            const SizedBox(height: 6),
            Text(
              busy ? '...' : (streaming ? 'Stop' : 'Talk'),
              style: interStyle(15, FontWeight.w700, color: color),
            ),
          ],
        ),
      ),
    );

    if (!enabled || busy) return Opacity(opacity: 0.55, child: button);

    return Semantics(
      button: true,
      label: streaming ? 'Stop streaming voice' : 'Start streaming voice',
      child: PressableButton(
        onTap: streaming ? onStop : onStart,
        child: button,
      ),
    );
  }
}

/// A panel showing a voice error.
class _ErrorPanel extends StatelessWidget {
  const _ErrorPanel({required this.message});

  final String message;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      accentBorder: true,
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Icon(Icons.error_outline, color: AppColors.error, size: 18),
          const SizedBox(width: AppTokens.gapSmall),
          Expanded(
            child: Text(
              message,
              style: interStyle(13, FontWeight.w400, color: AppColors.error),
            ),
          ),
        ],
      ),
    );
  }
}
