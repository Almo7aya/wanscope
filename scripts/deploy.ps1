# Development install: builds wanscope and copies it and the LuCI app to a
# router over SSH. Releases are installed from the packages CI builds.
#   .\scripts\deploy.ps1 [-Router root@192.168.1.1] [-GoArch arm64] [-UIOnly] [-DaemonOnly]
# The router can also come from $env:WANSCOPE_ROUTER or scripts\deploy.local.ps1
# (git-ignored; sets $Router).
param(
	[string]$Router = "",
	[string]$GoArch = "arm64",
	[string]$GoArm = "",
	[string]$GoMips = "",
	[switch]$UIOnly,
	[switch]$DaemonOnly
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if (-not $Router -and (Test-Path "$PSScriptRoot\deploy.local.ps1")) { . "$PSScriptRoot\deploy.local.ps1" }
if (-not $Router) { $Router = $env:WANSCOPE_ROUTER }
if (-not $Router) { throw "give -Router user@host" }

$stage = Join-Path ([IO.Path]::GetTempPath()) "wanscope-stage"
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $stage | Out-Null

if (-not $UIOnly) {
	$version = (Select-String -Path "$root\openwrt\wanscope\Makefile" -Pattern '^PKG_VERSION:=(.+)$').Matches[0].Groups[1].Value.Trim()
	$env:GOOS = "linux"; $env:GOARCH = $GoArch; $env:CGO_ENABLED = "0"
	if ($GoArm) { $env:GOARM = $GoArm }
	if ($GoMips) { $env:GOMIPS = $GoMips }
	Push-Location $root
	try { go build -trimpath -ldflags "-s -w -X main.version=$version-dev" -o "$stage\wanscope" ./cmd/wanscope }
	finally { Pop-Location }
	if ($LASTEXITCODE -ne 0) { throw "build failed" }
	Copy-Item "$root\openwrt\wanscope\files\wanscope.init" "$stage\wanscope.init"
	Copy-Item "$root\openwrt\wanscope\files\wanscope.config" "$stage\wanscope.config"
	# the data package's lists, when made here (go run ./cmd/wanscope-data ... openwrt/wanscope-data/data/...)
	$data = "$root\openwrt\wanscope-data\data"
	if ((Test-Path "$data\services.txt") -and (Test-Path "$data\owners.bin.gz")) {
		New-Item -ItemType Directory -Force "$stage\data" | Out-Null
		Copy-Item "$data\services.txt", "$data\owners.bin.gz" "$stage\data\"
	}
}
if (-not $DaemonOnly) {
	$luci = "$root\openwrt\luci-app-wanscope"
	Compress-Archive -Path "$luci\htdocs", "$luci\root" -DestinationPath "$stage\luci.zip"
}

# LF line endings for everything the router runs
Get-ChildItem $stage -Include *.init, *.config -Recurse | ForEach-Object {
	[IO.File]::WriteAllText($_.FullName, ((Get-Content $_.FullName -Raw) -replace "`r", ""))
}

$install = @'
set -e
cd /tmp/wanscope-stage
if [ -d data ]; then
	mkdir -p /usr/share/wanscope && cp data/services.txt data/owners.bin.gz /usr/share/wanscope/
fi
if [ -f wanscope ]; then
	/etc/init.d/wanscope stop 2>/dev/null || true
	install_bin() { cp "$1" "$2"; chmod 755 "$2"; }
	install_bin wanscope /usr/bin/wanscope
	install_bin wanscope.init /etc/init.d/wanscope
	[ -f /etc/config/wanscope ] || cp wanscope.config /etc/config/wanscope
	/etc/init.d/wanscope enable
	/etc/init.d/wanscope start
fi
if [ -f luci.zip ]; then
	rm -rf luci && mkdir luci && cd luci && unzip -oq ../luci.zip
	cp -r htdocs/* /www/
	cp -r root/* /
	cd ..
	rm -f /tmp/luci-indexcache* /tmp/luci-modulecache/* 2>/dev/null || true
	touch /usr/lib/opkg/status 2>/dev/null || true
	/etc/init.d/rpcd reload
fi
cd /tmp && rm -rf /tmp/wanscope-stage
echo installed
'@ -replace "`r", ""
[IO.File]::WriteAllText("$stage\install.sh", $install)

ssh $Router "rm -rf /tmp/wanscope-stage && mkdir -p /tmp/wanscope-stage"
scp -O -q -r "$stage\*" "${Router}:/tmp/wanscope-stage/"
ssh $Router "sh /tmp/wanscope-stage/install.sh"
