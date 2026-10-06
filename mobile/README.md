# Smart Remote

Flutter client for controlling a Windows PC running the Smart Remote server.
Run the server on the PC, connect both devices to the same Wi-Fi network, then
enter the PC's IP address and PIN in the app.

## Branding assets

The pairing and About screens use `assets/branding/logo.png`. Convert the
provided source JPEG to that asset from the repository root with PowerShell:

```powershell
$source = 'C:\Users\elmam\Desktop\assets-smart remote\smart-remote-logo.jpg'
$target = 'mobile\assets\branding\logo.png'
Add-Type -AssemblyName System.Drawing
$image = [System.Drawing.Image]::FromFile($source)
try {
    $image.Save($target, [System.Drawing.Imaging.ImageFormat]::Png)
} finally {
    $image.Dispose()
}
```

The Windows executable uses the same logo through a multi-size ICO and a
compiled Windows resource. From the repository root, run:

```powershell
Set-Location server
go run .\tools\mkicon -in ..\mobile\assets\branding\logo.png -out .\internal\remote\assets\smartremote.ico
windres -i winres.rc -O coff -o rsrc.syso
go build -o smart-remote-app.exe .
```

`windres` is provided by MinGW/MSYS2 and must be on `PATH`. The `.syso` is a
machine-specific build artifact and is intentionally ignored by Git; regenerate
it when building the Windows executable on another machine.
