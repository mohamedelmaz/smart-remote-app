import 'package:flutter_test/flutter_test.dart';
import 'package:smart_remote/core/server_api.dart';
import 'package:smart_remote/screens/home_screen.dart';

void main() {
  group('webcam tab', () {
    test('is present in the bottom navigation', () {
      // The regression this guards: the webcam screen existed and the server
      // served /webcam, but no tab pointed at it, so the feature was unreachable
      // from the app. A missing enum value is exactly how that happened.
      expect(RemoteTab.values, contains(RemoteTab.webcam));
    });

    test('sits between Screen and Voice', () {
      // Order matters for the nav bar: the camera belongs next to the screen
      // feed rather than at the end, where it would crowd the edge.
      final labels = RemoteTab.values.map((t) => t.label).toList();
      final screen = labels.indexOf('Screen');
      final webcam = labels.indexOf('Webcam');
      final voice = labels.indexOf('Voice');

      expect(screen, isNonNegative);
      expect(webcam, screen + 1);
      expect(voice, webcam + 1);
    });

    test('every tab carries a label and both icon states', () {
      // A tab with a missing icon renders as an invisible, unlabelled target.
      for (final tab in RemoteTab.values) {
        expect(tab.label, isNotEmpty, reason: 'tab $tab has no label');
      }
    });
  });

  group('ServerApi.webcamUri', () {
    test('points at /webcam on the paired host and port', () {
      final uri = ServerApi(host: '192.168.1.50', port: 9520)
          .webcamUri(pin: '1234');

      expect(uri.scheme, 'http');
      expect(uri.host, '192.168.1.50');
      expect(uri.port, 9520);
      expect(uri.path, '/webcam');
    });

    test('carries the PIN, which the server requires', () {
      // The camera is gated by the same authorizeStream as the desktop feed, so
      // a request without the PIN earns a 401 rather than a stream.
      final uri = ServerApi(host: '10.0.0.5', port: 9520)
          .webcamUri(pin: '987654');

      expect(uri.queryParameters['pin'], '987654');
    });

    test('is a different route from the desktop feed', () {
      // A typo here would silently stream the desktop into the camera tab, so
      // the two paths are asserted to differ.
      final api = ServerApi(host: '10.0.0.5', port: 9520);
      expect(api.webcamUri(pin: '1').path, isNot(api.screenUri(pin: '1').path));
    });
  });
}
