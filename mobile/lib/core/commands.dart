import 'dart:typed_data';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/remote_link.dart';
import '../state/providers.dart';

/// Typed helpers for sending commands to the PC.
///
/// Screens call these rather than assembling raw JSON, so command names stay
/// in one place and a server-side rename cannot silently break the UI.
class Remote {
  const Remote(this.link);

  final RemoteLink link;

  /// Whether commands can be sent right now.
  ///
  /// This is deliberately NOT the same as "a pairing exists". A pairing can
  /// outlive its socket - the PC reboots, Wi-Fi drops, the server is closed -
  /// and a [Remote] survives all of that. Treating "paired" as "connected" left
  /// every button on every screen looking enabled while silently discarding
  /// commands, which is what made the deck and shortcuts appear to "freeze"
  /// until the app was restarted.
  bool get connected => link.state == LinkState.ready;

  /// Whether a command can be attempted at all.
  ///
  /// [connected] is stricter and is what controls button affordance. This
  /// looser check exists for calls worth making anyway - notably the recovery
  /// commands that must work while the link is unhealthy.
  bool get usable => link.state != LinkState.idle &&
      link.state != LinkState.failed;

  /// Moves the cursor by a pixel delta. Fire-and-forget for latency.
  void moveCursor(int dx, int dy) =>
      link.send({'type': 'mouse.move', 'dx': dx, 'dy': dy});

  /// Moves the cursor to an absolute desktop coordinate.
  void moveCursorTo(int x, int y) =>
      link.send({'type': 'mouse.position', 'x': x, 'y': y});

  /// Full click, awaiting the ack so a failure can be reported.
  Future<Ack> click(String button) =>
      link.request({'type': 'mouse.click', 'button': button});

  Future<Ack> buttonDown(String button) =>
      link.request({'type': 'mouse.down', 'button': button});

  Future<Ack> buttonUp(String button) =>
      link.request({'type': 'mouse.up', 'button': button});

  /// Scrolls by notches; positive scrolls up.
  void scroll(int dx, int dy) => link
      .send({'type': 'mouse.scroll', 'wheelDx': dx, 'wheelDy': dy});

  /// Lifts any mouse button or modifier the PC still believes is held.
  ///
  /// Fire-and-forget on purpose: this is a recovery action taken in situations
  /// where the link may itself be broken, so waiting for an ack that will never
  /// arrive would defeat the purpose.
  void releaseInput() =>
      link.send({'type': 'input.release'});

  /// Taps a named key such as "enter" or "f5".
  Future<Ack> tapKey(String key) =>
      link.request({'type': 'key.tap', 'key': key});

  /// Presses a key while modifiers are held, e.g. Chord(['ctrl'], 'c').
  Future<Ack> chord(List<String> mods, String key) => link.request(
        {'type': 'key.chord', 'mods': mods, 'key': key},
      );

  /// Latches a modifier down or releases it.
  Future<Ack> latch(String key, bool down) =>
      link.request({'type': 'key.latch', 'key': key, 'down': down});

  /// Types text. The server injects it with KEYEVENTF_UNICODE, so it is
  /// keyboard-layout independent.
  Future<Ack> typeText(String text) =>
      link.request({'type': 'text.type', 'text': text});

  /// Runs a stored macro by id.
  Future<Ack> runMacro(String id) =>
      link.request({'type': 'macro.run', 'macroId': id});

  /// Sets the system volume, 0..100.
  Future<Ack> setVolume(int percent) =>
      link.request({'type': 'audio.volume', 'volume': percent});

  /// Adjusts the MJPEG stream settings.
  Future<Ack> configureStream({required int fps, required int quality}) =>
      link.request(
        {'type': 'stream.config', 'fps': fps, 'quality': quality},
      );

  /// Shows or hides the desktop cursor.
  Future<Ack> setCursorVisible(bool visible) =>
      link.request({'type': 'cursor.show', 'visible': visible});

  /// Starts PC-side audio playback for streamed voice audio.
  Future<Ack> startAudio() => link.request({'type': 'audio.start'});

  /// Stops PC-side audio playback.
  Future<Ack> stopAudio() => link.request({'type': 'audio.stop'});

  /// Streams one PCM chunk to the PC speakers.
  void streamAudio(List<int> pcm) =>
      link.sendAudio(pcm is Uint8List ? pcm : Uint8List.fromList(pcm));
}

/// The command helper, or null when not paired.
final remoteProvider = Provider<Remote?>((ref) {
  final link = ref.watch(remoteLinkProvider);
  return link == null ? null : Remote(link);
});
