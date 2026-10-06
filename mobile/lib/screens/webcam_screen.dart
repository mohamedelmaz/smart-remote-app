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
// HintText is declared in pairing_screen.dart rather than in the shared
// widgets, and screen_viewer_screen.dart imports it from the same place.
import 'pairing_screen.dart';

/// Live view of the PC's webcam, framed from the MJPEG stream.
///
/// This is deliberately the same pipeline as the desktop feed rather than a new
/// one: [MjpegFrameParser] parses `multipart/x-mixed-replace`,
/// `instantiateImageCodec` decodes off the build path, and the JPEG is shown
/// through [RawImage]. Only the route differs, because the server already
/// answers `/webcam` from the same framing and authentication code.
///
/// WHY THE CAMERA DOES NOT AUTO-START
/// ---------------------------------
/// The stream is started by a button rather than on entry. A camera is the most
/// private thing this app can show, and OS layers treat an unprompted camera
/// request as something to warn about. Making the user press Start also means
/// the tab can be opened, and left open, without pulling pictures off the PC.
class WebcamScreen extends ConsumerStatefulWidget {
  const WebcamScreen({super.key});

  @override
  ConsumerState<WebcamScreen> createState() => _WebcamScreenState();
}

/// Fires if the first frame has not arrived within this long.
///
/// Same rationale as the screen viewer: a firewall that silently drops the
/// request looks identical to a slow one unless a timer turns the stall into a
/// visible error.
const Duration _webcamFirstFrameTimeout = Duration(seconds: 12);

class _WebcamScreenState extends ConsumerState<WebcamScreen> {
  /// The most recently decoded frame, or null before the first one arrives.
  ui.Image? _frame;

  /// Changes on every decoded frame so the viewport repaints.
  int _sequence = 0;

  /// True while a stream request is in flight.
  bool _connecting = false;

  /// True once the stream has been started, so Stop is offered.
  bool _started = false;

  /// The last stream error, shown instead of an unexplained blank screen.
  String? _error;

  /// True when the PC answered "no camera", which is a normal state rather than
  /// a fault and gets its own wording and icon.
  bool _noCamera = false;

  /// Owns the in-flight request so it can be closed on exit.
  http.Client? _client;

  /// The subscription consuming the response body.
  StreamSubscription<List<int>>? _sub;

  /// Reused across chunks; reset on reconnect so stale bytes are not kept.
  final MjpegFrameParser _parser = MjpegFrameParser();

  /// True while a frame is being decoded off-thread.
  bool _decoding = false;

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
    if (mounted) {
      setState(() {
        _connecting = false;
        _started = false;
      });
    }
  }

  void _fail(String message, {bool noCamera = false}) {
    _firstFrameTimer?.cancel();
    _firstFrameTimer = null;
    _sub?.cancel();
    _sub = null;
    _client?.close();
    _client = null;
    if (!mounted) return;
    setState(() {
      _connecting = false;
      _started = false;
      _noCamera = noCamera;
      _error = message;
    });
  }
  /// Opens the webcam stream and renders each frame as it arrives.
  Future<void> _start() async {
    final pairing = ref.read(pairingProvider).valueOrNull;
    if (pairing == null) return;

    // The camera feed is authenticated exactly like the desktop feed: a camera
    // pointed at a desk is private, so a missing PIN is an actionable problem
    // rather than something to send anyway and let the server refuse.
    if (pairing.pin.isEmpty) {
      _fail(
        'No pairing PIN is stored. Re-pair with the PIN shown on the PC '
        'dashboard to see its camera.',
      );
      return;
    }

    _stop();
    setState(() {
      _connecting = true;
      _started = true;
      _error = null;
      _noCamera = false;
    });

    // Armed only while no picture has been seen. Once a frame exists a later
    // pause is normal, not a failure.
    _firstFrameTimer?.cancel();
    _firstFrameTimer = Timer(_webcamFirstFrameTimeout, () {
      if (!mounted || _frame != null) return;
      _fail(
        'No picture arrived from the PC. Check that port ${pairing.port} is '
        'allowed through the firewall on ${pairing.host}.',
      );
    });

    final client = http.Client();
    _client = client;
    final uri = ServerApi(host: pairing.host, port: pairing.port)
        .webcamUri(pin: pairing.pin);

    try {
      final response = await client.send(http.Request('GET', uri));
      if (response.statusCode == 401) {
        // Usually means the PIN was regenerated on the PC after this device
        // paired, so re-pairing is the actual fix.
        _fail(
          'The PC refused this device\'s PIN. If the PIN was regenerated on '
          'the PC, unpair and pair again.',
        );
        return;
      }

      // 404 is the server's answer for "this PC has no camera". It is answered
      // before the MJPEG headers are committed precisely so the phone can tell
      // it apart from a broken stream, and it is by far the most likely outcome
      // on a desktop with no webcam plugged in - so it must read as a normal
      // state, not an error.
      if (response.statusCode == 404) {
        _fail(
          'No camera detected on this PC. Plug in a webcam, or close the '
          'Windows Camera app if it is already using one.',
          noCamera: true,
        );
        return;
      }
      if (response.statusCode != 200) {
        // 409 means a previous session still holds the viewer lock: its
        // handler never returned (wedged capture) or the client vanished
        // without the server noticing. The lock self-heals after
        // ~30s of no frames, so the fix is to wait, not to restart.
        if (response.statusCode == 409) {
          _fail(
            'Another camera session is still active. Wait about 30 seconds '
            'and press Start again. If it persists, restart the PC server.',
          );
          return;
        }
        // 503 means the server answered but cannot capture right now
        // (camera busy in another app, or no interactive session).
        if (response.statusCode == 503) {
          _fail(
            'The PC could not capture its camera. Close the Windows Camera '
            'app or Teams if either is using it, then press Start again.',
          );
          return;
        }
        throw HttpException('server returned ${response.statusCode}');
      }

      _sub = response.stream.listen(
        _onChunk,
        onError: (Object error) {
          // A reset mid-stream is the server restarting or the network
          // dropping: distinct from "no picture yet" because frames may
          // already have been shown.
          final text = '$error'.toLowerCase();
          if (text.contains('reset by peer') ||
              text.contains('connection closed') ||
              text.contains('connection abort')) {
            _fail(
              'The PC closed the connection. Reconnecting usually fixes this: '
              'press Start again.',
            );
            return;
          }
          _fail('Stream error: $error');
        },
        onDone: () => _fail(
          _frame == null
              ? 'The PC closed the camera stream without sending a picture.'
              : 'The PC closed the camera stream.',
        ),
        cancelOnError: false,
      );
    } on SocketException catch (error) {
      _fail(
        'Could not reach the PC at ${pairing.host}:${pairing.port}. Check it '
        'is on the same Wi-Fi and the firewall allows port ${pairing.port} '
        '($error).',
      );
    } on TimeoutException {
      _fail(
        'The PC did not answer in time. Check it is on the same Wi-Fi and '
        'the firewall allows port ${pairing.port}.',
      );
    } catch (error) {
      _fail('Cannot reach the camera stream: $error');
    }
  }

  /// Handles one chunk of the stream body.
  void _onChunk(List<int> chunk) {
    final List<MjpegFrame> frames;
    try {
      frames = _parser.addChunk(chunk);
    } on MjpegFramingException catch (e) {
      // A stream that cannot be framed will never recover and its buffer keeps
      // growing, so tear the connection down rather than waiting politely for
      // a boundary that is not coming.
      _fail('${e.message} Restarting.');
      return;
    }

    if (frames.isEmpty || !mounted) return;

    // Backpressure. While a decode is in flight the frames behind it are
    // dropped: a live view only ever shows the newest frame, and queueing them
    // would make the picture lag further and further behind reality.
    if (_decoding) return;
    _decode(frames.last.bytes);
  }

  /// Decodes one JPEG off the build path.
  ///
  /// The codec and the image are released explicitly; leaking either once per
  /// frame would exhaust memory within a minute of watching.
  void _decode(Uint8List bytes) {
    _decoding = true;
    ui.instantiateImageCodec(bytes).then((codec) {
      if (!mounted) {
        codec.dispose();
        _decoding = false;
        return;
      }
      codec.getNextFrame().then((info) {
        codec.dispose();
        _decoding = false;
        if (!mounted) {
          // The viewer went away between the codec and the frame: release the
          // image here, because nothing downstream ever will.
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
          _sequence++;
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
  @override
  Widget build(BuildContext context) {
    final remote = ref.watch(remoteProvider);

    return Padding(
      padding: const EdgeInsets.all(AppTokens.gap),
      child: Column(
        children: [
          Expanded(child: _buildViewport()),
          const SizedBox(height: AppTokens.gap),
          _buildControls(remote != null),
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
            // RawImage draws an already-decoded ui.Image directly. Image.memory
            // would decode the JPEG again on the UI thread for every frame,
            // which at 15fps is enough to make the whole app feel stuck.
            //
            // AspectRatio preserves the camera's true geometry: letterboxing to
            // the phone's shape would misrepresent what the camera sees.
            : AspectRatio(
                key: ValueKey(_sequence),
                aspectRatio: frame.width / frame.height,
                child: RawImage(
                  image: frame,
                  // contain, not fill: stretching the picture would make it
                  // look distorted even though the camera is fine.
                  fit: BoxFit.contain,
                  filterQuality: FilterQuality.medium,
                ),
              ),
      ),
    );
  }

  /// Explains the current camera state in words.
  Widget _placeholder() {
    if (_noCamera) {
      // A camera icon rather than the generic "stream failed" icon, so the
      // message reads as "there is no camera" instead of "something broke".
      return const Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(Icons.videocam_off, size: 36, color: AppColors.textSecondary),
          SizedBox(height: AppTokens.gap),
          HintText('No camera detected on this PC.'),
        ],
      );
    }

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
          HintText('Connecting to the camera...'),
        ],
      );
    }

    return const Column(
      mainAxisAlignment: MainAxisAlignment.center,
      children: [
        Icon(Icons.photo_camera_outlined,
            size: 36, color: AppColors.textSecondary),
        SizedBox(height: AppTokens.gap),
        HintText('Tap Start to view the PC camera.'),
      ],
    );
  }

  /// Start and stop controls.
  Widget _buildControls(bool enabled) {
    return GlassPanel(
      padding: const EdgeInsets.all(AppTokens.gapSmall),
      child: Row(
        children: [
          Expanded(
            flex: 2,
            child: RemoteButton(
              label: _started ? 'Restart' : 'Start',
              icon: _started ? Icons.refresh : Icons.play_arrow,
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
              // Enabled only while something is actually running, so the button
              // cannot be pressed into a state that does not exist.
              onPressed: enabled && _started ? _stop : null,
            ),
          ),
        ],
      ),
    );
  }
}
