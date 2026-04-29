$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
New-Item -ItemType Directory -Force -Path (Join-Path $Root "app\bin"), (Join-Path $Root "dist") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $Root "app\ui\web") | Out-Null
Copy-Item -Path (Join-Path $Root "ui\*") -Destination (Join-Path $Root "app\ui\web") -Recurse -Force

$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"

go build -trimpath -ldflags="-s -w" -o (Join-Path $Root "app\bin\fn-badblocks-victoria") (Join-Path $Root "src")

$CmdFiles = @(
  "main",
  "install_init",
  "install_callback",
  "uninstall_init",
  "uninstall_callback",
  "upgrade_init",
  "upgrade_callback",
  "config_init",
  "config_callback"
)
foreach ($File in $CmdFiles) {
  $Path = Join-Path $Root "cmd\$File"
  if (Test-Path $Path) {
    icacls $Path /grant "*S-1-1-0:RX" | Out-Null
  }
}

$LocalFnpack = Join-Path $Root "fnpack.exe"
if (Test-Path $LocalFnpack) {
  & $LocalFnpack build --directory $Root
} elseif (Get-Command fnpack -ErrorAction SilentlyContinue) {
  fnpack build --directory $Root
} else {
  Write-Warning "fnpack not found; binary built at app\bin\fn-badblocks-victoria. Install fnpack from the fnOS developer toolkit, then rerun this script."
}
