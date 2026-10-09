import 'dart:async';
import 'dart:io';
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:http/http.dart' as http;

import '../core/commands.dart';
import '../core/server_api.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import '../widgets/zoomable_frame.dart';
import 'pairing_screen.dart';

/// Live view of the PC's screen, framed from the MJPEG stream.
///
/// WHY THIS IS NOT `Image.network`
/// ------------------------------
/// An MJPEG response is not an image: it is an endless sequence of
/// `multipart/x-mixed-replace` parts, and no image decoder will accept it. So
/// the socket is read here, the framing is parsed, and each complete JPEG is
/// decoded through the engine's own codec.
///
/// WHY DECODING IS OFF-THREAD AND RATE-LIMITED
/// -------------------------------------------
/// A full-screen JPEG takes far longer to decode than the server takes to
/// produce the next one. Decoding on the UI thread therefore builds a backlog
/// that grows without limit, and the viewer ends up showing frames seconds
/// behind the PC while the app appears to hang. `instantiateImageCodec` decodes
/// off the build path, and a frame arriving while another is still decoding is
/// dropped rather than queued, because a mirror only ever shows the newest
/// frame anyway.
class ScreenViewerScreen extends ConsumerStatefulWidget {
  const ScreenViewerScreen({super.key});

  @override
  ConsumerState<ScreenViewerScreen> createState() =>
      _ScreenViewerScreenState();
}

/// Fires if the first frame has not arrived within this long.
///
/// Without it a failed connect is indistinguishable from a slow one: the status
/// stays "connecting" and the user gets no error, no spinner and no way to tell
/// that anything went wrong. A stall is the most common real failure here - a
/// firewall blocking the port, a Wi-Fi that drops the route - and it is
/// precisely the one a bare `catch` cannot see, because the socket never errors,
/// it simply never produces anything.
const Duration _firstFrameTimeout = Duration(seconds: 12);

class _ScreenViewerScreenState extends ConsumerState<ScreenViewerScreen> {
  /// Frame rate requested from the server.
  int _fps = 15;

  /// JPEG quality requested from the server.
  int _quality = 70;

  /// The most recently decoded frame, or null before the first one arrives.
  ui.Image? _frame;

  /// True while a stream request is in flight.
  bool _connecting = false;

  /// The last stream error, shown instead of an unexplained blank screen.
  String? _error;

  /// Owns the in-flight request so it can be closed on exit.
  http.Client? _client;

  /// The subscription consuming the response body.
  StreamSubscription<List<int>>? _sub;

  /// Reused across chunks; reset on reconnect so stale bytes are not kept.
  final MjpegFrameParser _parser = MjpegFrameParser();

  /// True while a frame is being decoded off-thread.
  bool _decoding = false;

  /// Frames skipped because a decode was already running.
  int _dropped = 0;

  /// The stall watchdog, armed only until the first frame lands.
  Timer? _firstFrameTimer;

  @override
  void dispose() {
    _stop();
    super.dispose();
  }

  /// Closes the stream request and releases the socket.
  void _stop() {
    _firstFrameTimer?.cancel();
    _firstFrameTimer = null;
    _sub?.cancel();
    _sub = null;
    _client?.close();
    _client = null;
    _parser.reset();
    // The displayed image is a native resource; releasing it here is what stops
    // a repeatedly reopened viewer leaking one per session.
    final frame = _frame;
    _frame = null;
    frame?.dispose();
    if (mounted) setState(() => _connecting = false);
  }

  void _fail(String message) {
    _firstFrameTimer?.cancel();
    _firstFrameTimer = null;
    _sub?.cancel();
    _sub = null;
    _client?.close();
    _client = null;
    if (!mounted) return;
    setState(() {
      _connecting = false;
      _error = message;
    });
  }

  /// Opens the MJPEG stream and renders each frame as it arrives.
  Future<void> _start() async {
    final pairing = ref.read(pairingProvider).valueOrNull;
    if (pairing == null) return;

    // The desktop feed is authenticated, like the command channel: it is the
    // whole screen of the PC. A pairing saved before this requirement existed
    // may carry an empty PIN, which would only ever earn a 401, so that is
    // reported as the actionable problem it is.
    if (pairing.pin.isEmpty) {
      _fail(
        'No pairing PIN is stored. Re-pair with the PIN shown on the PC '
        'dashboard to watch its screen.',
      );
      return;
    }

    _stop();
    setState(() {
      _connecting = true;
      _error = null;
      _dropped = 0;
    });

    // Armed only while no picture has been seen. A stream that already
    // delivered a frame has nothing to wait for, and a brief pause later is
    // normal rather than a failure.
    _firstFrameTimer?.cancel();
    _firstFrameTimer = Timer(_firstFrameTimeout, () {
      if (!mounted || _frame != null) return;
      _fail(
        'No picture arrived from the PC. Check that port ${pairing.port} is '
        'allowed through the firewall on ${pairing.host}.',
      );
    });

    final client = http.Client();
    _client = client;
    final uri = ServerApi(host: pairing.host, port: pairing.port).screenUri(
          pin: pairing.pin,
          fps: _fps,
          quality: _quality,
        );

    try {
      final response = await client.send(http.Request('GET', uri));
      if (response.statusCode == 401) {
        // The PIN is wrong, which usually means it was regenerated on the PC
        // after this device paired. Saying so is far more useful than a bare
        // status code, because re-pairing is the actual fix.
        _fail(
          'The PC refused this device\'s PIN. If the PIN was regenerated on '
          'the PC, unpair and pair again.',
        );
        return;
      }
      if (response.statusCode != 200) {
        throw HttpException('server returned ${response.statusCode}');
      }

      _sub = response.stream.listen(
        _onChunk,
        onError: (Object error) => _fail('Stream error: $error'),
        // A closed stream is normal when the PC stops the capture or the
        // server shuts down, so it is reported rather than thrown. The two
        // cases are worded differently because "never sent a picture" points at
        // capture or the firewall, while "stopped mid-stream" does not.
        onDone: () => _fail(
          _frame == null
              ? 'The PC closed the screen stream without sending a picture.'
              : 'The PC closed the screen stream.',
        ),
        cancelOnError: false,
      );
    } catch (error) {
      _fail('Cannot reach the screen stream: $error');
    }
  }

  /// Handles one chunk of the stream body.
  void _onChunk(List<int> chunk) {
    final List<MjpegFrame> frames;
    try {
      frames = _parser.addChunk(chunk);
    } on MjpegFramingException catch (e) {
      // A stream that cannot be framed will never recover, and the buffer
      // behind it keeps growing. Tear the connection down rather than waiting
      // politely for a boundary that is not coming.
      _fail('${e.message} Restarting.');
      return;
    }

    if (frames.isEmpty || !mounted) return;

    // Backpressure. Decoding a full-screen JPEG is slower than the server
    // produces frames, so without this the parser extracts frames far faster
    // than the codec retires them and the pending work piles up until the
    // device is killed. A mirror only ever shows the newest frame, so while a
    // decode is in flight the ones behind it are simply dropped.
    if (_decoding) {
      _dropped += frames.length;
      return;
    }
    _decode(frames.last.bytes);
  }

  /// Decodes one JPEG off the build path.
  ///
  /// Both the codec and the image are released explicitly. A codec holds a
  /// native decoder and a full-screen image is several MB, so leaking either
  /// once per frame crashes the app within a minute of watching.
  void _decode(Uint8List bytes) {
    _decoding = true;
    ui.instantiateImageCodec(bytes).then((codec) {
      if (!mounted) {
        codec.dispose();
        _decoding = false;
        return;
      }
      codec.getNextFrame().then((info) {
        // The codec has been consumed; releasing it here is what keeps the
        // native decoder from accumulating one instance per frame.
        codec.dispose();
        _decoding = false;
        if (!mounted) {
          // The viewer went away between the codec and the frame: release it
          // here, because nothing downstream ever will.
          info.image.dispose();
          return;
        }
        // The watchdog is stood down the moment a real picture exists, so a
        // later pause is never misreported as a connection failure.
        _firstFrameTimer?.cancel();
        _firstFrameTimer = null;
        setState(() {
          final previous = _frame;
          _frame = info.image;
          previous?.dispose();
        });
      }).catchError((Object error) {
        codec.dispose();
        _decoding = false;
        if (mounted) _fail('Could not decode a frame: $error');
      });
    }).catchError((Object error) {
      _decoding = false;
      if (mounted) _fail('Could not decode a frame: $error');
    });
  }

  /// Applies new fps or quality and reconnects.
  Future<void> _applySettings({int? fps, int? quality}) async {
    if (fps != null) _fps = fps;
    if (quality != null) _quality = quality;

    final remote = ref.read(remoteProvider);
    if (remote != null) {
      // The settings are pushed to the PC as well as being sent in the stream
      // URL, so a later reconnect uses the same capture settings.
      final ack = await remote.configureStream(fps: _fps, quality: _quality);
      if (!ack.ok && ack.error.isNotEmpty && mounted) {
        ScaffoldMessenger.of(context)
            .showSnackBar(SnackBar(content: Text(ack.error)));
      }
    }
    await _start();
  }

  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);
    final status = ref.watch(serverStatusProvider).valueOrNull;

    return Padding(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        children: [
          Expanded(child: _buildViewport()),
          const SizedBox(height: AppTokens.gap),
          _buildControls(remote != null, status?.streamBusy ?? false),
        ],
      ),
    );
  }

  /// The frame area, or a placeholder explaining why there is no picture.
  Widget _buildViewport() {
    final frame = _frame;

    return GlassPanel(
      padding: const EdgeInsets.all(AppTokens.gapSmall),
      child: Center(
        child: frame == null
            ? _placeholder()
            // ZoomableFrame owns the InteractiveViewer transform so pinch, pan and
            // double-tap survive per-frame rebuilds. It must NOT carry a
            // changing key: recreating it per frame would reset the zoom.
            // RawImage draws an already-decoded ui.Image directly. Image.memory
            // would decode the JPEG again on the UI thread for every frame,
            // which at 15fps of a full desktop is enough to make the whole app
            // feel stuck.
            //
            // The AspectRatio inside preserves the PC's true geometry: letter-
            // boxing to the phone's shape would misrepresent the screen, and
            // the user needs real proportions to judge what they are clicking
            // through the touchpad.
            : SizedBox.expand(child: ZoomableFrame(frame: frame)),
      ),
    );
  }

  /// Explains the current stream state in words.
  Widget _placeholder() {
    if (_error != null) {
      return Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          const Icon(Icons.videocam_off, size: 36, color: AppColors.error),
          const SizedBox(height: AppTokens.gap),
          HintText(_error!),
        ],
      );
    }

    if (_connecting) {
      return const Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          SizedBox(
            width: 28,
            height: 28,
            child: CircularProgressIndicator(color: AppColors.accent),
          ),
          SizedBox(height: AppTokens.gap),
          HintText('Connecting to the screen stream...'),
        ],
      );
    }

    return const Column(
      mainAxisAlignment: MainAxisAlignment.center,
      children: [
        Icon(Icons.desktop_windows_outlined,
            size: 36, color: AppColors.textSecondary),
        SizedBox(height: AppTokens.gap),
        HintText('Tap Start to view the PC screen.'),
      ],
    );
  }
/// Start, stop and stream-quality controls.
  Widget _buildControls(bool enabled, bool streamBusy) {
    return Column(
      children: [
        GlassPanel(
          padding: const EdgeInsets.all(AppTokens.gapSmall),
          child: Row(
            children: [
              Expanded(
                flex: 2,
                child: RemoteButton(
                  label: _frame == null && !_connecting ? 'Start' : 'Restart',
                  icon: _frame == null ? Icons.play_arrow : Icons.refresh,
                  expand: true,
                  onPressed: enabled ? _start : null,
                ),
              ),
              const SizedBox(width: AppTokens.gapSmall),
              Expanded(
                child: RemoteButton(
                  label: 'Stop',
                  icon: Icons.stop,
                  expand: true,
                  onPressed: enabled && _frame != null ? _stop : null,
                ),
              ),
            ],
          ),
        ),
        const SizedBox(height: AppTokens.gapSmall),
        GlassPanel(
          padding: const EdgeInsets.symmetric(
              horizontal: AppTokens.gap, vertical: 4),
          child: Column(
            children: [
              _SliderRow(
                label: 'FPS',
                value: _fps.toDouble(),
                min: 1,
                max: 30,
                // 30 is the practical ceiling for a phone over WiFi; beyond
                // that frames queue up and the view lags behind the PC.
                onChanged:
                    enabled ? (v) => setState(() => _fps = v.round()) : null,
                onChangeEnd:
                    enabled ? (v) => _applySettings(fps: v.round()) : null,
              ),
              _SliderRow(
                label: 'Quality',
                value: _quality.toDouble(),
                min: 10,
                max: 100,
                onChanged:
                    enabled ? (v) => setState(() => _quality = v.round()) : null,
                onChangeEnd:
                    enabled ? (v) => _applySettings(quality: v.round()) : null,
              ),
            ],
          ),
        ),
        if (streamBusy) ...[
          const SizedBox(height: AppTokens.gapSmall),
          // The server allows one viewer at a time. Saying so explains a black
          // screen that would otherwise look like a bug in the app.
          const HintText('Another device is already viewing this screen.'),
        ],
        // A sustained drop count is the visible sign the phone cannot keep up
        // with the requested frame rate. Surfacing it lets the user lower FPS
        // themselves instead of just concluding the app is broken.
        if (_frame != null && _dropped > 30) ...[
          const SizedBox(height: AppTokens.gapSmall),
          HintText(
            'Dropped $_dropped frames - this phone is struggling at $_fps fps. '
            'Try a lower frame rate.',
          ),
        ],
      ],
    );
  }
}

/// A labelled slider used for the stream settings.
class _SliderRow extends StatelessWidget {
  const _SliderRow({
    required this.label,
    required this.value,
    required this.min,
    required this.max,
    required this.onChanged,
    required this.onChangeEnd,
  });

  final String label;
  final double value;
  final double min;
  final double max;
  final ValueChanged<double>? onChanged;
  final ValueChanged<double>? onChangeEnd;

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        SizedBox(
          width: 56,
          child: Text(label, style: interStyle(13, FontWeight.w600)),
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
              value: value.clamp(min, max),
              min: min,
              max: max,
              onChanged: onChanged,
              // onChangeEnd reconnects once, so dragging a slider does not
              // open a new stream request on every frame.
              onChangeEnd: onChangeEnd,
            ),
          ),
        ),
        SizedBox(
          width: 36,
          child: Text(
            value.round().toString(),
            textAlign: TextAlign.right,
            style: monoStyle(12, FontWeight.w700, color: AppColors.accent),
          ),
        ),
      ],
    );
  }
}
