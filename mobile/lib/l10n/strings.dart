import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// The languages the app ships with.
enum AppLang {
  /// English. Left-to-right.
  en,

  /// Arabic. Right-to-left.
  ar;

  bool get isRtl => this == AppLang.ar;
}

/// Key under which the chosen language is persisted.
const _kLangKey = 'smart_remote.lang';

/// Reads the saved language, defaulting to English.
Future<AppLang> loadLang() async {
  final prefs = await SharedPreferences.getInstance();
  final raw = prefs.getString(_kLangKey);
  for (final l in AppLang.values) {
    if (l.name == raw) return l;
  }
  return AppLang.en;
}

/// The active language. Changing it rebuilds every translated widget.
final langProvider = StateNotifierProvider<LangNotifier, AppLang>((ref) {
  return LangNotifier()..restore();
});

/// Holds and persists the language choice.
class LangNotifier extends StateNotifier<AppLang> {
  LangNotifier() : super(AppLang.en);

  /// Loads the persisted choice without blocking first paint.
  Future<void> restore() async {
    final saved = await loadLang();
    if (saved != state) state = saved;
  }

  /// Switches language and remembers it for the next launch.
  Future<void> set(AppLang lang) async {
    if (lang == state) return;
    state = lang;
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_kLangKey, lang.name);
  }

  /// Flips between the two languages.
  Future<void> toggle() => set(state.isRtl ? AppLang.en : AppLang.ar);
}

/// Translation lookup for the current language.
///
/// Every string exists in both languages from the start rather than falling
/// back at runtime: a missing translation then shows up as an obvious empty
/// label in development instead of silently leaking English into an Arabic UI.
class _Strings {
  const _Strings(this.lang);

  final AppLang lang;

  /// Picks the Arabic string when the app is in Arabic.
  String text(String en, String ar) => lang.isRtl ? ar : en;
}

/// The translation table for the surrounding widget tree.
class T {
  T(AppLang lang) : _s = _Strings(lang);

  final _Strings _s;

  /// Builds the lookup table for a widget subtree.
  static T of(BuildContext context) {
    // Watching the provider is what makes a language change rebuild every
    // caller. Reading it without watching would leave the old language on
    // screen until something else happened to rebuild.
    final lang = ProviderScope.containerOf(context).read(langProvider);
    return T(lang);
  }

  String text(String en, String ar) => _s.text(en, ar);

  String get mediaKeys => text('Media keys', 'مفاتيح الوسائط');
  String get volume => text('Volume', 'مستوى الصوت');
  String get desktopShortcuts => text('Desktop shortcuts', 'اختصارات سطح المكتب');

  String get pad => text('Pad', 'لوحة التحكم');
  String get media => text('Media', 'الوسائط');
  String get deck => text('Deck', 'الاختصارات');
  String get keys => text('Keys', 'لوحة المفاتيح');
  String get screen => text('Screen', 'الشاشة');
  String get voice => text('Voice', 'الصوت');

  String get connected => text('Connected', 'متصل');
  String get pairing => text('Pairing…', 'جارٍ الإقران…');
  String get reconnecting => text('Reconnecting…', 'جارٍ إعادة الاتصال…');
  String get pinRejected => text('PIN rejected', 'رمز الدخول مرفوض');
  String get offline => text('Offline', 'غير متصل');
  String get reconnectNow => text('Reconnect now', 'إعادة الاتصال الآن');
  String get unpair => text('Unpair this PC', 'إلغاء إقران هذا الحاسوب');

  String get appKeyboard => text('App keyboard', 'لوحة مفاتيح التطبيق');
  String get phoneKeyboard => text('Phone keyboard', 'لوحة مفاتيح الهاتف');
  String get howToUse => text('How to use', 'طريقة الاستخدام');
  String get about => text('About', 'حول المشروع');

  String get notConnected => text('Not connected to the PC.', 'غير متصل بالحاسوب.');
  String get reconnectingToast =>
      text('Not connected to the PC. Reconnecting…',
          'غير متصل بالحاسوب. جارٍ إعادة الاتصال…');
  String get notAccepted => text('The PC did not accept it', 'لم يقبل الحاسوب الأمر');
  String get macroFailed => text('Macro failed', 'فشل تنفيذ الاختصار');

  String get howToUseBody => text(
        '1. Run the server on the computer.\n'
        '2. Make sure both devices are connected to the same Wi-Fi network.\n'
        '3. Enter the IP address and PIN to connect.',
        '1. شغّل الخادم على الكمبيوتر.\n'
        '2. تأكد من اتصال كلا الجهازين بنفس شبكة Wi-Fi.\n'
        '3. أدخل عنوان IP ورمز PIN للاتصال.',
      );

  String get aboutBody => text(
      'Smart Remote Control v1.0.0. Developed by elmamo. A professional, '
      'zero-latency LAN remote control solution.',
      'Smart Remote Control v1.0.0. طوّره elmamo. حل احترافي للتحكم عن بُعد '
      'عبر الشبكة المحلية بدون أي تأخير.');
  String get license => text('Released under the MIT License.',
      'مُتاح بموجب رخصة MIT.');

  // --- About / How to use sheet -----------------------------------------
  // These back the two sections the info button opens, plus the language
  // toggle. They live here rather than inline so the sheet has no hardcoded
  // strings that would stay English in an Arabic UI.
  String get info => text('Info', 'معلومات');
  String get close => text('Close', 'إغلاق');
  String get language => text('Language', 'اللغة');
  String get arabic => text('العربية', 'العربية');
  String get english => text('English', 'English');
  String get stepsTitle => text('Three steps to connect', 'ثلاث خطوات للاتصال');
  String get aboutTitle => text('About', 'حول المشروع');
  String get developedBy => text('Developed by', 'طوّره');
  String get version => text('Version', 'الإصدار');
}

/// The [Locale] matching [lang], for Flutter's own text layout.
Locale localeFor(AppLang lang) =>
    lang.isRtl ? const Locale('ar') : const Locale('en');