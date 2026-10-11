# Smart Remote

Professional zero-latency LAN remote control: a Windows tray server plus a
Flutter phone app (touchpad, keyboard, screen view, macros, voice, webcam).

## Requirements

- Windows 10 or 11, 64-bit

## Layout

- `server/` — Go tray server + embedded web dashboard. Build with
  `server/build.ps1` (runs tests, regenerates the icon, verifies the GUI
  subsystem). See `server/build.ps1` for the documented steps.
- `mobile/` — Flutter client. See `mobile/README.md`.
- `index.html` + `images/` + `styles.css` — public website (GitHub Pages).
- `assets-smart remote/` — local-only design material (ignored), except the
  logo sources under `description_picture/logo-svg/` and `logo-new/`.

## Build from a clean checkout

```powershell
Set-Location server
go test ./...
.\build.ps1
```

```powershell
Set-Location mobile
flutter pub get
flutter test
flutter build apk --release
```

Release binaries (`.exe`, `.apk`, `.syso`, logs) are intentionally ignored by
Git; rebuild them with the steps above.

## Optional security lock

Off by default. When you turn it on from the tray, the web dashboard and the
tray menu stop showing the pairing PIN and the pairing address until the code
is typed. The code is stored separately from the pairing PIN and is never
derived from it, so changing one does not change the other. Phones on the
network are unaffected and pair exactly as before.

## Known limits

- The published APK is signed with a debug key, not a release key.
- The security lock does not protect against a phone on the same network, and
  it cannot stop the process from being ended in Task Manager.
- Use it only on a network you trust.
- SmartScreen may warn because the binary is unsigned; allow the app in the
  firewall on a private network.

## Rules of the project

Think, then verify, then apply — and stop on any doubt. Connection safety
and current quality never regress for a new feature.

## License

MIT — see `LICENSE`. © 2026 elmamo.
