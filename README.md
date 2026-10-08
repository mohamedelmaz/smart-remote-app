# Smart Remote

Professional zero-latency LAN remote control: a Windows tray server plus a
Flutter phone app (touchpad, keyboard, screen view, macros, voice, webcam).

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

## Rules of the project

Think, then verify, then apply — and stop on any doubt. Connection safety
and current quality never regress for a new feature.

## License

MIT — see `LICENSE`. © 2026 elmamo.
