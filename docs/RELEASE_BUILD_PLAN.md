# Release build plan

This describes how a Smart Remote release is produced today, and what is planned
next. Everything under "planned" is not built yet.

## Current process (manual)

The release is built and published by hand, on the maintainer's machine.

1. **Server executable**

   ```powershell
   Set-Location server
   .\build.ps1
   ```

   This regenerates the icon, compiles the Windows resources, runs `go test ./...`,
   builds `smart-remote-app.exe` as a GUI-subsystem binary and verifies the
   subsystem. It fails the build if any test fails.

2. **Android APK**

   ```powershell
   Set-Location mobile
   flutter analyze
   flutter test
   flutter build apk --release
   ```

3. **Checksums.** The two artefacts are copied to a staging folder outside the
   repository under the release asset names, and a `SHA256SUMS.txt` is generated:

   ```
   <sha256>  smart-remote-app.exe
   <sha256>  app-release.apk
   ```

4. **Publish.** Commit, tag, push, create the GitHub release with the assets
   above, then download the assets back and compare their hashes against
   `SHA256SUMS.txt` before publishing the download links on the website.

The APK is signed with a debug key. It is not suitable for store distribution.

## Planned: GitHub Actions build

Planned, not implemented. The intended workflow:

- Trigger: manual, and on a published tag.
- Runner: `windows-latest`.
- Run `go test ./...` in `server/` and `flutter analyze` / `flutter test` in
  `mobile/`.
- Build the executable with the same steps `build.ps1` performs today, using the
  same linker flags, and build the APK.
- Produce `SHA256SUMS.txt` in the same step.
- Upload both artefacts as a workflow artefact.
- Submit the executable to SignPath for signing through the SignPath GitHub
  Action.
- Wait for a manual approval by the maintainer before the signing step runs.
- Publish the release with the signed executable, the APK and the checksums.
- Verify the published asset hashes in the same job and fail it on any mismatch.

The version is asserted identical across all release surfaces by
`TestVersionIsTheSameEverywhere` in `server/internal/remote/version_test.go`, so a
tagged build cannot ship a binary whose version disagrees with the sources.

## Planned: installer

Planned, not implemented. The intended behaviour:

- Install the executable into the user's per-program location.
- Create the inbound firewall rules during installation, restricted to the private
  network profile, and remove them on uninstall. Today the rules are created with
  the `Any` profile and are not removed when the server quits.
- Register an uninstall entry so removal leaves no trace in the firewall or in the
  registry.
- Offer the start-with-Windows option as a checkbox, defaulting to off.

## Known gaps

- No continuous integration yet. Every build is made manually, so nothing verifies
  a change until a human runs it.
- No installer or uninstaller yet. Removal is the manual procedure described in
  [CODE_SIGNING_POLICY.md](CODE_SIGNING_POLICY.md).
- The Android APK is debug-signed.
- No code signing yet. The SignPath application is pending approval.
- The runtime font download from Google's servers is still present in both the
  phone app and the PC dashboard. Bundling the fonts would remove it.