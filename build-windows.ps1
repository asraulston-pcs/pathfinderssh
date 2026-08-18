# build-windows.ps1
#
# Build the PathfinderSSH front ends natively on Windows (run this ON Windows,
# in PowerShell).
#
# Place this at the repo root (same directory as go.mod and cmd\).
# Run:   .\build-windows.ps1                            # every front end under cmd\
#        .\build-windows.ps1 -Targets pathfinder,pfvault
#        .\build-windows.ps1 -Targets gui               # only the Fyne front ends
#        .\build-windows.ps1 -Targets cli               # only the console tools
#        .\build-windows.ps1 -Strip:$false              # keep symbols (debugging)
#        .\build-windows.ps1 -Console                   # force a console on the GUI apps too
#        .\build-windows.ps1 -Version v0.93             # override the stamped version
#        .\build-windows.ps1 -Tags paid                 # pass build tags through
#
# Build deps: Go + a C compiler on PATH (Fyne needs CGO). Easiest options:
#   - MSYS2:  pacman -S mingw-w64-x86_64-gcc   (then add C:\msys64\mingw64\bin to PATH)
#   - or TDM-GCC / WinLibs mingw-w64.
# Verify with:  gcc --version
#
# TARGET DISCOVERY: every directory under cmd\ that holds Go source is a target,
# so adding or removing a front end needs no edit here.
#
# GUI vs CLI matters MORE here than on the other two platforms: -H windowsgui
# detaches the process from a console, and a console tool built that way prints
# nothing at all -- crawl, capture, reach and pfvault would run silently and
# look hung. So the flag is decided per target by whether the dependency graph
# reaches fyne.io/fyne/v2/app, which is the only import that pulls glfw and cgo.
# -Console forces a console on everything, which is how you read a GUI app's
# stdout when it misbehaves in the field.
#
# VERSION: stamped with -X main.version=<git describe>. The linker silently
# ignores -X for a package with no such symbol, so it is safe to pass to every
# target even though only cmd\pathfinder reads it today (Help > About).
#
# Signing: nothing here signs anything. Store-published builds are re-signed by
# Microsoft; a direct download needs its own certificate.

param(
    [string[]]$Targets = @("all"),
    [bool]$Strip       = $true,
    [switch]$Console,
    [string]$Version   = "",
    [string]$Tags      = ""
)

$ErrorActionPreference = "Stop"
# PowerShell 7.4+ turns a non-zero native exit code into a terminating error when
# ErrorActionPreference is Stop. This script checks $LASTEXITCODE itself so it can
# report every failing target instead of dying on the first one -- and so a plain
# "git describe" in a non-repo tree is not fatal. Assigning this on 5.1 just
# creates an unused variable.
$PSNativeCommandUseErrorActionPreference = $false
Set-Location $PSScriptRoot

$Out = "dist\windows"
New-Item -ItemType Directory -Force -Path $Out | Out-Null

if ([string]::IsNullOrWhiteSpace($Version)) {
    try {
        $Version = (& git describe --tags --always --dirty 2>$null | Select-Object -First 1)
    } catch {
        $Version = ""
    }
    if ([string]::IsNullOrWhiteSpace($Version)) { $Version = "0.93" } else { $Version = $Version.Trim() }
}

$env:CGO_ENABLED = "1"
$env:GOOS        = "windows"

$buildFlags = @("-trimpath")
if (-not [string]::IsNullOrWhiteSpace($Tags)) { $buildFlags += @("-tags", $Tags) }

# --- target discovery -------------------------------------------------------

$all = @()
foreach ($d in (Get-ChildItem -Path "cmd" -Directory -ErrorAction SilentlyContinue)) {
    if (Get-ChildItem -Path $d.FullName -Filter "*.go" -File -ErrorAction SilentlyContinue) {
        $all += $d.Name
    }
}

if ($all.Count -eq 0) {
    Write-Host "!! no buildable directories under cmd\ -- is this the repo root?"
    exit 1
}

function Test-Gui([string]$app) {
    try {
        $deps = & go list -deps "./cmd/$app" 2>$null
    } catch {
        return $false
    }
    if ($LASTEXITCODE -ne 0) { return $false }
    return [bool]($deps | Where-Object { $_ -eq "fyne.io/fyne/v2/app" })
}

$select = $Targets
$list = @()
if ($select.Count -eq 1 -and $select[0] -eq "all") {
    $list = $all
} elseif ($select.Count -eq 1 -and $select[0] -eq "gui") {
    $list = @($all | Where-Object { Test-Gui $_ })
} elseif ($select.Count -eq 1 -and $select[0] -eq "cli") {
    $list = @($all | Where-Object { -not (Test-Gui $_) })
} else {
    $list = $select
}

if ($list.Count -eq 0) {
    Write-Host "!! nothing selected (-Targets $($select -join ','))"
    exit 1
}

# --- build ------------------------------------------------------------------

$arch = (go env GOARCH)
Write-Host ">> version $Version, arch $arch"

$failed = @()
foreach ($app in $list) {
    if (-not (Test-Path "cmd\$app")) {
        Write-Host "!! no such target: cmd\$app"
        $failed += $app
        continue
    }

    $gui = Test-Gui $app
    if ($gui) { $kind = "gui" } else { $kind = "cli" }

    $ld = @()
    if ($Strip) { $ld += "-s"; $ld += "-w" }
    # -H windowsgui hides the background console window for a GUI app; a console
    # tool built with it loses stdout entirely, so it is applied per target.
    if ($gui -and -not $Console) { $ld += "-H"; $ld += "windowsgui" }
    $ld += "-X"; $ld += "main.version=$Version"
    $ldflags = ($ld -join " ")

    Write-Host ">> building $app.exe ($kind)  ldflags='$ldflags'"
    & go build @buildFlags -ldflags "$ldflags" -o "$Out\$app.exe" "./cmd/$app"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "!! FAILED: $app"
        $failed += $app
    }
}

Write-Host ">> done: $Out"
Get-ChildItem $Out -Filter *.exe | Select-Object Name, Length, FullName

if ($failed.Count -gt 0) {
    Write-Host (">> FAILED: " + ($failed -join ", "))
    exit 1
}