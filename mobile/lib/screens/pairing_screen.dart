import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../models/models.dart';
import '../models/pairing.dart';
import '../state/discovery.dart';
import '../state/providers.dart';
import '../theme/app_theme.dart';
import '../widgets/glass.dart';
import 'home_screen.dart';
import 'info_sheet.dart';

/// Pairing screen: automatic discovery plus manual entry.
class PairingScreen extends ConsumerStatefulWidget {
  const PairingScreen({super.key});

  @override
  ConsumerState<PairingScreen> createState() => _PairingScreenState();
}

class _PairingScreenState extends ConsumerState<PairingScreen> {
  final _hostCtrl = TextEditingController();
  final _pinCtrl = TextEditingController();
  final _nameCtrl = TextEditingController();

  bool _busy = false;
  bool _scanning = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    // Prefill the phone name so a returning user sees (and can keep) it.
    // Async by nature (disk read); guarded by mounted as always.
    ref.read(pairingProvider.notifier).loadDeviceName().then((name) {
      if (mounted && _nameCtrl.text.isEmpty) _nameCtrl.text = name;
    });
  }

  @override
  void dispose() {
    _hostCtrl.dispose();
    _pinCtrl.dispose();
    _nameCtrl.dispose();
    super.dispose();
  }

  /// Pairs using manual entry.
  Future<void> _pairManually() async {
    final host = _hostCtrl.text.trim();
    if (host.isEmpty) {
      setState(() => _error = 'Enter the PC address shown on its dashboard.');
      return;
    }

    setState(() {
      _busy = true;
      _error = null;
    });

    // The port is fixed by the server build, so it is not user-editable.
    final pairing = Pairing(
      host: host,
      port: kDefaultPort,
      pin: _pinCtrl.text.trim(),
      label: host,
    );
    // Persisted before pairing so _connect picks it up in the auth frame.
    await ref.read(pairingProvider.notifier).setDeviceName(_nameCtrl.text);
    final error = await ref.read(pairingProvider.notifier).pair(pairing);

    if (!mounted) return;
    setState(() => _busy = false);
    if (error != null) {
      setState(() => _error = error);
      return;
    }
    _goHome();
  }

  /// Pairs with a device found by discovery.
  Future<void> _pairWith(DiscoveredDevice device) async {
    setState(() {
      _busy = true;
      _error = null;
    });

    final pairing = Pairing(
      host: device.host,
      port: device.port == 0 ? kDefaultPort : device.port,
      // A discovered device carries its PIN in the TXT record, so the common
      // case needs no typing at all.
      pin: device.pin,
      label: device.name,
    );
    await ref.read(pairingProvider.notifier).setDeviceName(_nameCtrl.text);
    final error = await ref.read(pairingProvider.notifier).pair(pairing);

    if (!mounted) return;
    setState(() => _busy = false);
    if (error != null) {
      setState(() => _error = error);
      return;
    }
    _goHome();
  }

  void _goHome() {
    Navigator.of(context).pushReplacement(
      MaterialPageRoute(builder: (_) => const HomeScreen()),
    );
  }

  /// Sweeps the local subnet for PCs that are not advertising over mDNS.
  Future<void> _scanSubnet() async {
    setState(() => _scanning = true);
    await ref.read(discoveryProvider.notifier).scanSubnet();
    if (mounted) setState(() => _scanning = false);
  }


  @override
  Widget build(BuildContext context) {
    final discovery = ref.watch(discoveryProvider);
    final diagnostics = ref.watch(diagnosticsProvider);

    return Scaffold(
      body: SafeArea(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(AppTokens.gap),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SizedBox(height: AppTokens.gap),
              // The brand mark and the info button are the only chrome on this
              // screen: everything else is the pairing form itself. The logo is
              // decorative, so it carries an empty semantics label rather than
              // being announced as an unlabelled image.
              Stack(
                alignment: Alignment.topCenter,
                children: [
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.center,
                    children: [
                      const Center(child: BrandLogo()),
                      const SizedBox(height: AppTokens.gap),
                      Center(
                        child: Text('Smart Remote',
                            style: interStyle(22, FontWeight.w700)),
                      ),
                      const SizedBox(height: AppTokens.gapSmall),
                      Text(
                        'Find your PC, then use the PIN it shows on its '
                        'dashboard.',
                        textAlign: TextAlign.center,
                        style: interStyle(14, FontWeight.w400,
                            color: AppColors.textSecondary),
                      ),
                    ],
                  ),
                  Align(
                    alignment: AlignmentDirectional.topEnd,
                    child: IconButton(
                      tooltip: 'Info',
                      icon: const Icon(Icons.info_outline, size: 22),
                      color: AppColors.textSecondary,
                      onPressed: () => showInfoSheet(context),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 28),

              SectionTitle(
                label: 'Nearby devices',
                trailing: _scanning
                    ? const SizedBox(
                        width: 16,
                        height: 16,
                        child: CircularProgressIndicator(
                          strokeWidth: 2,
                          color: AppColors.accent,
                        ),
                      )
                    : TextButton(
                        onPressed: _scanSubnet,
                        child: const Text('Scan network'),
                      ),
              ),
              const SizedBox(height: AppTokens.gapSmall),

              GlassPanel(
                child: discovery.when(
                  loading: () => const HintText(
                    'Searching for PCs on this network…',
                  ),
                  error: (_, _) => const HintText(
                    'Automatic discovery is unavailable here. Enter the '
                    'address manually below.',
                  ),
                  data: (devices) => devices.isEmpty
                      ? const HintText(
                          'No devices found yet. Make sure the PC server is '
                          'running and both devices are on the same WiFi.',
                        )
                      : Column(
                          children: [
                            for (final device in devices)
                              Padding(
                                padding: const EdgeInsets.only(
                                    bottom: AppTokens.gapSmall),
                                child: DeviceTile(
                                  device: device,
                                  onTap:
                                      _busy ? null : () => _pairWith(device),
                                ),
                              ),
                          ],
                        ),
                ),
              ),

              const SizedBox(height: 28),
              const SectionTitle(label: 'Enter manually'),
              const SizedBox(height: AppTokens.gapSmall),

              GlassPanel(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    TextField(
                      controller: _hostCtrl,
                      keyboardType: TextInputType.url,
                      autocorrect: false,
                      style: monoStyle(15, FontWeight.w400),
                      decoration: const InputDecoration(
                        labelText: 'PC address',
                        hintText: '192.168.1.10',
                        prefixIcon: Icon(Icons.lan_outlined),
                      ),
                    ),
                    const SizedBox(height: AppTokens.gapSmall),
                    TextField(
                      controller: _pinCtrl,
                      keyboardType: TextInputType.number,
                      maxLength: 6,
                      style: monoStyle(15, FontWeight.w700),
                      decoration: const InputDecoration(
                        labelText: 'PIN',
                        hintText: '6 digits',
                        prefixIcon: Icon(Icons.pin_outlined),
                        counterText: '',
                      ),
                    ),
                    const SizedBox(height: AppTokens.gapSmall),
                    TextField(
                      controller: _nameCtrl,
                      autocorrect: false,
                      maxLength: 64,
                      style: interStyle(15, FontWeight.w400),
                      decoration: const InputDecoration(
                        labelText: 'Device name (optional)',
                        hintText: "e.g. Ahmed's phone",
                        prefixIcon: Icon(Icons.smartphone_outlined),
                        counterText: '',
                      ),
                    ),
                    const SizedBox(height: AppTokens.gap),
                    RemoteButton(
                      label: _busy ? 'Connecting…' : 'Connect',
                      icon: Icons.link,
                      expand: true,
                      onPressed: _busy ? null : _pairManually,
                    ),
                  ],
                ),
              ),


              if (_error != null) ...[
                const SizedBox(height: AppTokens.gap),
                GlassPanel(
                  accentBorder: true,
                  child: Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Icon(Icons.error_outline,
                          color: AppColors.error, size: 18),
                      const SizedBox(width: AppTokens.gapSmall),
                      Expanded(
                        child: Text(
                          _error!,
                          style: interStyle(13, FontWeight.w400,
                              color: AppColors.error),
                        ),
                      ),
                    ],
                  ),
                ),
              ],

              // Troubleshooting advice comes from the server's own
              // diagnostics, so it reflects the real machine state rather
              // than generic guesses.
              diagnostics.when(
                loading: () => const SizedBox.shrink(),
                error: (_, _) => const SizedBox.shrink(),
                data: (diag) => (diag == null || diag.advice.isEmpty)
                    ? const SizedBox.shrink()
                    : Padding(
                        padding: const EdgeInsets.only(top: 28),
                        child: Column(
                          crossAxisAlignment: CrossAxisAlignment.stretch,
                          children: [
                            const SectionTitle(label: 'Connection help'),
                            const SizedBox(height: AppTokens.gapSmall),
                            GlassPanel(child: AdviceList(advice: diag.advice)),
                          ],
                        ),
                      ),
              ),
              const SizedBox(height: 32),
            ],
          ),
        ),
      ),
    );
  }
}

/// A section heading with an optional trailing action.
class SectionTitle extends StatelessWidget {
  const SectionTitle({super.key, required this.label, this.trailing});

  final String label;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        Text(
          label.toUpperCase(),
          style: interStyle(
            12,
            FontWeight.w700,
            color: AppColors.textSecondary,
          ),
        ),
        const Spacer(),
        // The null-aware marker skips the null entirely, which is what an
        // optional trailing widget means here.
        ?trailing,
      ],
    );
  }
}

/// One discovered PC.
class DeviceTile extends StatelessWidget {
  const DeviceTile({super.key, required this.device, this.onTap});

  final DiscoveredDevice device;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    return GlassPanel(
      onTap: onTap,
      accentBorder: onTap != null,
      padding: const EdgeInsets.all(12),
      child: Row(
        children: [
          Container(
            width: 40,
            height: 40,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: AppColors.accent.withValues(alpha: 0.15),
            ),
            child: const Icon(Icons.computer, color: AppColors.accent, size: 20),
          ),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(device.name,
                    style: interStyle(15, FontWeight.w600),
                    overflow: TextOverflow.ellipsis),
                Text(
                  '${device.host}:${device.port}',
                  style: monoStyle(12, FontWeight.w400,
                      color: AppColors.textSecondary),
                ),
              ],
            ),
          ),
          if (device.pin.isNotEmpty)
            Text(device.pin,
                style: monoStyle(13, FontWeight.w700, color: AppColors.accent)),
        ],
      ),
    );
  }
}


/// A muted explanatory message.
class HintText extends StatelessWidget {
  const HintText(this.text, {super.key});

  final String text;

  @override
  Widget build(BuildContext context) {
    return Text(
      text,
      style: interStyle(13, FontWeight.w400, color: AppColors.textSecondary),
    );
  }
}

/// A bulleted list of server-supplied troubleshooting advice.
///
/// The bullet icon is decorative; each item also has real text, so the list
/// is readable without relying on the icon.
class AdviceList extends StatelessWidget {
  const AdviceList({super.key, required this.advice});

  final List<String> advice;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final item in advice)
          Padding(
            padding: const EdgeInsets.only(bottom: AppTokens.gapSmall),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                const Icon(Icons.info_outline,
                    size: 16, color: AppColors.warning),
                const SizedBox(width: AppTokens.gapSmall),
                Expanded(
                  child: Text(
                    item,
                    style: interStyle(13, FontWeight.w400,
                        color: AppColors.textSecondary),
                  ),
                ),
              ],
            ),
          ),
      ],
    );
  }
}
