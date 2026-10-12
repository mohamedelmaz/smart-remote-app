# Code signing policy

## Scope

This document describes the code signing policy for **Smart Remote**, the Windows
tray server in the `server/` directory of
[mohamedelmaz/smart-remote-app](https://github.com/mohamedelmaz/smart-remote-app).

Only the Windows executable `smart-remote-app.exe` is covered. The Android APK is
a separate artefact with a separate release process and is **not** signed under
this policy.

## Signing service

Smart Remote uses the free code signing service offered by **SignPath.io**.
The certificate is issued by the **SignPath Foundation**.

**Status: applied for, pending approval. No signing certificate is active yet.**
Until approval is granted, release binaries are unsigned and Windows SmartScreen
may warn when they are started.

The sentence required by SignPath for use in the product documentation is:

> Free code signing provided by SignPath.io, certificate by SignPath Foundation.

It will be added to the README and to the project website **only after** approval
is granted.

## Project identity

- Project name: **Smart Remote**. The tray, the dashboard and the website all use
  that name. The executable's own file properties still carry the older string
  *Smart Remote Control* as the product and file description; that is a leftover
  and has not been changed.
- Repository: `mohamedelmaz/smart-remote-app`.
- The product version is identical across all release surfaces. It is enforced by
  a test, `TestVersionIsTheSameEverywhere` in
  `server/internal/remote/version_test.go`, which fails the build if the version
  in `server/internal/remote/brand.go`, the numeric `FILEVERSION` and
  `PRODUCTVERSION` fields in `server/winres.rc`, the string table in the same
  file, `mobile/pubspec.yaml` and `mobile/lib/screens/info_sheet.dart` ever
  disagree. The same test is part of the release build.

## Roles

| Role | Person |
|---|---|
| Author | mohamedelmaz |
| Reviewer | mohamedelmaz |
| Approver | mohamedelmaz |

One maintainer currently holds all three roles. Enabling two-factor authentication
on the GitHub account is **to be confirmed by the maintainer**.

## Build and signing process

1. The executable is built from the source in this repository with
   `server/build.ps1`. This is a local, manual build today. It runs the Go test
   suite, regenerates the icon, compiles the Windows resources and verifies that
   the produced binary has the GUI subsystem.
2. Signing requests are approved manually by the maintainer. There is no
   automatic signing step.
3. A GitHub Actions workflow that builds on `windows-latest`, runs the tests and
   submits the artefact to SignPath is **planned**, not in place. The plan is
   described in [RELEASE_BUILD_PLAN.md](RELEASE_BUILD_PLAN.md).

## Privacy policy

### What the product does with your data

Smart Remote is a local-network remote control. **It contains no telemetry, no
analytics, no crash reporting, no advertising, no update checking and no account
system. Nothing is sent to any cloud service, because there is no cloud service.**

The PC server makes no outbound connections of its own. It answers requests on the
local network and does not contact any remote host.

The phone app stores no data about you and has no user account. It talks to the PC
you pair with, and to nothing else.

One exception is worth stating plainly rather than hiding. Both the phone app and
the PC dashboard display the open-source fonts *Inter* and *JetBrains Mono*, which
are loaded at runtime from Google's font servers (`fonts.googleapis.com`). This is
an ordinary font download. The request reveals the IP address of the device doing
the loading and the user-agent of the browser or the app, and nothing else from
this project: no file contents, no keystrokes, no screen content, no pairing
material. It applies to whichever device loads the page or runs the app, so
opening the dashboard on the PC exposes the PC's IP address to Google, and opening
the app on the phone exposes the phone's. The fonts can be bundled into both to
remove the request entirely.

### What is stored on the PC

All state is kept locally under `%APPDATA%\SmartRemote`, next to the log file
that the server writes beside its executable (`smart-remote-server.log`).

These are the only files the server creates. There is no settings file for anything
else: stream quality, frame rate, device name and the control port are command line
options that take their default value on every start unless you pass them yourself.

| Item | File | Contents |
|---|---|---|
| Pairing PIN | `%APPDATA%\SmartRemote\pin` | The PIN, in plain text, with owner-only file permissions. |
| Security lock state | `%APPDATA%\SmartRemote\settings.json` | A format version number, and the lock's enabled flag together with a random salt, the derived key hash and the iteration count. The lock code itself is never written: what is stored is a salted PBKDF2-HMAC-SHA256 hash of it. |
| Macros | `%APPDATA%\SmartRemote\macros.json` | The macro list you can edit. |
| Blocked devices | `%APPDATA%\SmartRemote\blocklist.json` | The addresses you blocked from the dashboard. |
| Server log | `smart-remote-server.log`, beside the executable | Plain-text log. Described below. |

The log is plain text and contains the address and name of paired devices. It also
contains the pairing PIN, unless you have turned the optional security lock on, in
which case the PIN is not written. Treat the log file as sensitive and delete it
if you do not want it kept.

Two further facts about the log, stated plainly. It is opened in append mode and is
never rotated, trimmed or given a size limit, so it grows for as long as the server
runs and nothing removes old lines for you. Its file permissions are ordinary
rather than owner-only, so other user accounts on the same PC can read it, which is
another reason to delete it if you do not want the pairing PIN lying around.

### What the server changes on the PC

- **A Windows Firewall rule.** Two inbound rules named *Smart Remote Server* and
  *Smart Remote Server mDNS* are created on first start, allowing inbound TCP on
  the control port and inbound UDP for mDNS discovery. Both are applied to the
  `Any` network profile, so they are not restricted to private networks. They are
  not removed when you quit the server.
- **A startup entry, only if you ask for it.** The tray menu can add a
  `HKCU\...\CurrentVersion\Run` entry so the server starts with Windows. It is off
  by default and can be switched off again from the same menu.
- No other system change is made. Nothing is installed outside the folder above
  and the executable itself.

### How to remove it today

There is no installer and no uninstaller yet; both are planned.

To remove Smart Remote manually:

1. Quit the server from its tray icon.
2. Delete the executable.
3. Delete `%APPDATA%\SmartRemote` and `smart-remote-server.log`.
4. In Windows Defender Firewall, remove the two rules named *Smart Remote Server*
   and *Smart Remote Server mDNS*.
5. If you enabled start-with-Windows, remove the *Smart Remote* entry from
   `HKCU\...\CurrentVersion\Run`, or toggle it off from the tray menu before
   quitting.

On the phone, uninstalling the app removes its local data with it.

### Who can reach the server

The server listens on TCP port 9520 on all network interfaces, so any device that
can route to your PC on the local network can reach it. It also advertises itself
over mDNS on the local network, which is how the phone finds it.

Access is protected by the pairing PIN, and the strength of that protection should
be described accurately: **the PIN is a convenience for a network you trust, not a
control against a hostile device on your network.** The PIN is not a secret held
only between you and your phone. It is published on the local network as part of
automatic discovery, and it is also returned by the server's status endpoint, so
anything able to reach the server can read it. Some of the server's web endpoints
answer requests that carry no credential at all while the optional security lock is
off, which is the default. The correct assumption is that anyone able to observe
or reach this traffic on your network is able to read it. Treat the PIN as
adequate for keeping casual access away on a home network, and as inadequate for
anything stronger.

The endpoints that can change the machine's security posture are restricted to
this PC. Reading the lock state, unlocking, locking again, setting a code,
turning the lock off, running a tray action (which includes regenerating the
pairing PIN), and POST /api/pin are all refused with "only available on this PC"
unless the request arrives from this machine: either over the loopback
interface, or from one of this PC's own network addresses, which is what keeps
the dashboard working when you open it as `http://192.168.x.x:9520` rather than
through `127.0.0.1`. Another device on the network is refused whatever address
it uses.

Device management in the dashboard is under a narrower restriction still:
blocking and unblocking a phone answers only over the loopback interface, so a
dashboard opened as http://192.168.x.x:9520 can see the blocked list but cannot
change it. Open it as http://127.0.0.1:9520, which is what the tray does, to use
those buttons.

Reachability is not decided from the Origin header, because that header is
simply absent from any client that is not a browser; some of these endpoints
additionally refuse a mismatched Origin.

You can block individual devices from the dashboard, and the block list survives
restarts.

Nothing is reachable from the internet unless you deliberately forward the port on
your router.

## Planned improvements

These are planned. None of them is implemented today, and no date is given because
none is committed to.

- **Bundle the fonts locally.** Ship *Inter* and *JetBrains Mono* inside the phone
  app and inside the dashboard instead of downloading them from Google's servers, so
  that opening either one makes no outbound request at all.
- **Keep the PIN out of the log.** Write only that a PIN exists, never its value, so
  the log file stops being something to protect.
- **Bound the log.** The log is opened in append mode and is never rotated or
  trimmed, so it can grow without limit while the server runs. Add a size limit and
  keep a few rotated files.
- **Installer with a proper uninstall entry.** Install the firewall rules during
  installation, restricted to the private network profile, offer start-with-Windows
  as an off-by-default option, and register an uninstaller that removes the rules,
  the registry entry and the configuration folder.
- **GitHub Actions build.** Build on `windows-latest`, run the tests, submit the
  artefact to SignPath and wait for a manual approval before anything is signed or
  published. Described in [RELEASE_BUILD_PLAN.md](RELEASE_BUILD_PLAN.md).
- **Stop publishing the PIN and require a session.** The PIN is still published in
  the discovery advertisement and still returned by the status endpoint today, and
  the endpoints that read or change it are still reachable without any credential
  from this PC.
  The plan is to stop putting it into either place and to require an authenticated
  session on every API and WebSocket endpoint. The session would be established when
  the user enters the PIN on the phone, so pairing stays as easy as it is now while
  the PIN stops being readable by anything merely present on the network.
- **Make the executable's product name match the project name.** The file
  properties currently say *Smart Remote Control* while the product is called
  *Smart Remote* everywhere else.

## Contact

Please raise an issue on the repository:
<https://github.com/mohamedelmaz/smart-remote-app/issues>