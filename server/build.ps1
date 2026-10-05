<#
.SYNOPSIS
    Builds the Smart Remote server as a silent tray application.

.DESCRIPTION
    Produces server/smart-remote-app.exe with three things a plain `go build`
    does not give you, each of which the user would otherwise notice as a bug:

      1. GUI subsystem (-H windowsgui), so Windows does not allocate a console
         window. This is the console that follows the cursor when dragged and
         sits over everything else: a tray app has no business showing one.
      2. Stripped symbols (-s -w), roughly halving the binary.
      3. The PE resources (icon, manifest, version info) compiled from
         winres.rc, so Explorer and the taskbar show the right logo and the
         file properties report elmamo / 1.0.0.0.

    The icon is regenerated from the source logo first, so a new logo needs no
    manual step.

.PARAMETER NoTest
    Skip the test suite. Only for a quick local iteration; a release build
    should run the tests.

.EXAMPLE
    .\build.ps1
#>
[CmdletBinding()]
param(
    [switch]$NoTest
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

# --- 1. Source logo -> multi-resolution .ico -------------------------------
# Keeping this in the build is what stops the icon drifting from the logo: the
# .ico is a generated artefact, not something to remember to regenerate.
$logo = 'C:\Users\elmam\Desktop\assets-smart remote\smart-remote-logo.jpg'
$ico  = 'internal\remote\assets\smartremote.ico'

if (Test-Path $logo) {
    Step 'Generating the icon from the logo'
    go run ./tools/mkicon -in $logo -out $ico
} else {
    Write-Warning "logo not found at $logo; using the existing .ico"
}

# --- 2. Compile the PE resources -------------------------------------------
# windres comes from MSYS2. Without it the build still succeeds but the
# executable gets a default icon and no version information.
$windres = Get-Command windres -ErrorAction SilentlyContinue
if ($windres) {
    Step 'Compiling the Windows resources (icon, manifest, version)'
    & windres -i winres.rc -O coff -o rsrc.syso
} else {
    Write-Warning 'windres not found; the .exe will have no icon or version info'
}

# --- 3. Tests --------------------------------------------------------------
if (-not $NoTest) {
    Step 'Running the Go tests'
    go test ./...
}

# --- 4. The build itself ----------------------------------------------------
# -H windowsgui is the important flag: without it this is a console program and
# Windows shows a window the user cannot get rid of.
#   -s  strip the symbol table and debug info
#   -w  omit the DWARF debug sections
Step 'Building smart-remote-app.exe (GUI subsystem, stripped)'
go build -ldflags '-s -w -H windowsgui' -o smart-remote-app.exe .

# --- 5. Confirm the subsystem actually changed ------------------------------
# Reading the PE header is the only way to be sure: the flag is silently ignored
# on a non-Windows target, and a build that "succeeded" while still being a
# console program is exactly the bug this script exists to prevent.
Step 'Verifying the PE subsystem is GUI (2), not console (3)'
$bytes = [System.IO.File]::ReadAllBytes((Resolve-Path 'smart-remote-app.exe'))
$peOffset = [BitConverter]::ToInt32($bytes, 0x3C)
$subsystem = [BitConverter]::ToUInt16($bytes, $peOffset + 0x5C)
if ($subsystem -eq 2) {
    Write-Host '    OK: GUI subsystem, no console window will appear' -ForegroundColor Green
} else {
    Write-Error "PE subsystem is $subsystem, expected 2 (GUI). A console window will appear."
}

$size = [math]::Round((Get-Item 'smart-remote-app.exe').Length / 1MB, 2)
Write-Host "`nBuilt smart-remote-app.exe ($size MB)" -ForegroundColor Green
Write-Host 'Run it from the tray. No window is expected.' -ForegroundColor DarkGray