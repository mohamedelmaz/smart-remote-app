import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smart_remote/core/commands.dart';
import 'package:smart_remote/core/remote_link.dart';
import 'package:smart_remote/state/providers.dart';

void main() {
  test('command consumers track socket disconnects and reconnects', () async {
    final link = _TestRemoteLink();
    final container = ProviderContainer(
      overrides: [remoteLinkProvider.overrideWith((ref) => link)],
    );
    final subscription = container.listen(remoteProvider, (_, _) {});

    addTearDown(subscription.close);
    addTearDown(container.dispose);
    addTearDown(link.close);

    expect(container.read(remoteProvider)?.connected, isFalse);

    link.emit(LinkState.ready);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(remoteProvider)?.connected, isTrue);

    link.emit(LinkState.reconnecting);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(remoteProvider)?.connected, isFalse);

    link.emit(LinkState.ready);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(remoteProvider)?.connected, isTrue);
  });
}

class _TestRemoteLink extends RemoteLink {
  _TestRemoteLink() : super(host: '127.0.0.1', port: 0);

  final StreamController<LinkState> _stateController =
      StreamController<LinkState>.broadcast(sync: true);
  LinkState _state = LinkState.connecting;

  @override
  LinkState get state => _state;

  @override
  Stream<LinkState> get states => _stateController.stream;

  void emit(LinkState state) {
    _state = state;
    _stateController.add(state);
  }

  Future<void> close() => _stateController.close();
}
