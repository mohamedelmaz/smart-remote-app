import 'dart:ui';

import 'package:flutter/material.dart';

import '../theme/app_theme.dart';

/// A frosted glass panel.
///
/// Uses a real [BackdropFilter] so the blur composites with what is behind
/// it, which is what makes the glassmorphism read as depth rather than a flat
/// translucent rectangle.
class GlassPanel extends StatelessWidget {
  const GlassPanel({
    super.key,
    required this.child,
    this.padding = const EdgeInsets.all(AppTokens.gap),
    this.borderRadius = AppTokens.radius,
    this.onTap,
    this.accentBorder = false,
    this.margin,
  });

  final Widget child;
  final EdgeInsetsGeometry padding;
  final double borderRadius;

  /// When set, the panel becomes tappable with a press animation.
  final VoidCallback? onTap;

  /// Draws a cyan border, used to mark the active or selected panel.
  final bool accentBorder;
  final EdgeInsetsGeometry? margin;

  @override
  Widget build(BuildContext context) {
    final radius = BorderRadius.circular(borderRadius);

    Widget content = Container(
      margin: margin,
      padding: padding,
      decoration: BoxDecoration(
        color: AppColors.glass,
        borderRadius: radius,
        border: Border.all(
          color: accentBorder
              ? AppColors.accent.withValues(alpha: 0.55)
              : AppColors.glassBorder,
        ),
        boxShadow:
            accentBorder ? AppTokens.glow(opacity: 0.22, blur: 18) : null,
      ),
      child: child,
    );

    if (onTap != null) {
      // The press scale gives tactile feedback that a colour change alone
      // cannot, and it is visible to users who cannot see the cyan glow.
      content = PressableButton(onTap: onTap!, child: content);
    }

    return ClipRRect(
      borderRadius: radius,
      child: BackdropFilter(
        filter: ImageFilter.blur(sigmaX: 14, sigmaY: 14),
        child: content,
      ),
    );
  }
}

/// Scales its child down briefly while pressed.
class PressableButton extends StatefulWidget {
  const PressableButton({super.key, required this.child, required this.onTap});

  final Widget child;
  final VoidCallback onTap;

  @override
  State<PressableButton> createState() => _PressableButtonState();
}

class _PressableButtonState extends State<PressableButton> {
  bool _pressed = false;

  @override
  Widget build(BuildContext context) {
    return GestureDetector(
      behavior: HitTestBehavior.opaque,
      onTapDown: (_) => setState(() => _pressed = true),
      onTapUp: (_) => setState(() => _pressed = false),
      onTapCancel: () => setState(() => _pressed = false),
      onTap: widget.onTap,
      child: AnimatedScale(
        scale: _pressed ? 0.96 : 1,
        duration: AppTokens.transition,
        curve: AppTokens.curve,
        child: widget.child,
      ),
    );
  }
}

/// A labelled control used across the remote screens.
///
/// Every control carries a text label. Icons are decorative accents only: an
/// icon font can fail to load and a screen reader announces only the label, so
/// a control identified by an icon alone would be unusable in those cases.
class RemoteButton extends StatelessWidget {
  const RemoteButton({
    super.key,
    required this.label,
    this.icon,
    this.onPressed,
    this.active = false,
    this.danger = false,
    this.compact = false,
    this.expand = false,
  });

  /// The visible text. Always present.
  final String label;

  /// Optional decorative icon.
  final IconData? icon;
  final VoidCallback? onPressed;

  /// Shows the cyan neon glow to indicate a latched or active state.
  final bool active;

  /// Uses the error colour, for destructive actions such as Unpair.
  final bool danger;
  final bool compact;

  /// Fills the available width, used inside grids.
  final bool expand;

  @override
  Widget build(BuildContext context) {
    final enabled = onPressed != null;
    final accent = danger ? AppColors.error : AppColors.accent;

    final background =
        active ? accent.withValues(alpha: 0.18) : AppColors.glassStrong;
    final borderColor =
        active ? accent.withValues(alpha: 0.7) : AppColors.glassBorder;
    final foreground = !enabled
        ? AppColors.textSecondary
        : (active || danger ? accent : AppColors.textPrimary);

    final button = AnimatedContainer(
      duration: AppTokens.transition,
      curve: AppTokens.curve,
      padding: EdgeInsets.symmetric(
        horizontal: compact ? 12 : 18,
        vertical: compact ? 10 : 16,
      ),
      decoration: BoxDecoration(
        color: background,
        borderRadius: BorderRadius.circular(AppTokens.radiusSmall),
        border: Border.all(color: borderColor),
        boxShadow: active ? AppTokens.glow(opacity: 0.3) : null,
      ),
      child: Row(
        mainAxisSize: expand ? MainAxisSize.max : MainAxisSize.min,
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          if (icon != null) ...[
            Icon(icon, size: compact ? 16 : 20, color: foreground),
            const SizedBox(width: AppTokens.gapSmall),
          ],
          // Flexible prevents an overflow when a label is long next to an
          // icon, or in a narrow grid cell.
          Flexible(
            child: Text(
              label,
              maxLines: 2,
              textAlign: TextAlign.center,
              overflow: TextOverflow.ellipsis,
              style: interStyle(
                compact ? 12 : 14,
                FontWeight.w600,
                color: foreground,
              ),
            ),
          ),
        ],
      ),
    );

    if (!enabled) {
      return Opacity(opacity: 0.55, child: button);
    }

    return PressableButton(
      onTap: onPressed!,
      child: expand ? SizedBox(width: double.infinity, child: button) : button,
    );
  }
}

/// A circular button for directional and D-pad input.
///
/// Circular buttons carry a direction label, because a bare arrow glyph is
/// ambiguous to a screen reader and easy to mis-tap.
class DpadButton extends StatelessWidget {
  const DpadButton({
    super.key,
    required this.label,
    required this.onPressed,
    this.icon,
    this.diameter = 64,
  });

  final String label;
  final VoidCallback? onPressed;
  final IconData? icon;
  final double diameter;

  @override
  Widget build(BuildContext context) {
    final enabled = onPressed != null;
    return Semantics(
      button: true,
      label: label,
      child: SizedBox(
        width: diameter,
        height: diameter,
        child: PressableButton(
          onTap: onPressed ?? () {},
          child: Opacity(
            opacity: enabled ? 1 : 0.5,
            child: Container(
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: AppColors.glassStrong,
                border:
                    Border.all(color: AppColors.accent.withValues(alpha: 0.35)),
                boxShadow: AppTokens.glowSubtle(),
              ),
              alignment: Alignment.center,
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  if (icon != null)
                    Icon(icon, size: 20, color: AppColors.accent),
                  if (icon != null) const SizedBox(height: 2),
                  Text(
                    label,
                    style: interStyle(
                      10,
                      FontWeight.w600,
                      color: AppColors.textPrimary,
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}
