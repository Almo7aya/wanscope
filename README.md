# wanscope

A bandwidth monitor for OpenWrt routers that know more than one way out: several WANs or SIMs, VPN tunnels,
load balancing. It shows, live and over time, **which device used which uplink for what**, how much data is
left on each data plan, and it can limit, schedule, cap or block devices.

wanscope is a small Go daemon with LuCI pages. It reads what the router already knows (conntrack and the
interface counters) instead of counting packets on one interface, so a device's traffic is counted once
with the uplink it actually left through, even on one-armed routers and with mwan3.

## What it shows

- **Overview**: download and upload right now, stacked by uplink, second by second; the share of each
  uplink; the devices moving data now; what is left of each data plan; what used the internet today.
- **Devices**: every client with its live speed, today and this month, the uplinks it used and what it
  mostly did. Each device has its own page: live and historic charts by uplink, services, sites and
  addresses, open connections, and its controls. Names come from DHCP, static leases, NetBIOS and mDNS,
  and you can rename any device and give it an emoji.
- **Uplinks and data plans**: each WAN counted on the link itself (IP bytes, as carriers count), the
  last 30 days, who and what used it, and the tunnels it carries. A data plan groups the uplinks that
  draw from one bundle (several SIMs or APNs on one modem share one): prepaid (buy a bundle, use it up,
  buy again) or monthly. It shows what is left, the pace, and when it runs out at that pace. "I bought
  more" starts a new bundle; "Correct it" matches the count to what the carrier says.
- **Connections**: every open connection with its device, server name, service, uplink and speed.
- **History**: any range, per uplink or per device, in 10-minute, daily or monthly bars, with the
  services and sites behind it, exportable as CSV.
- **Tunnels**: a WireGuard tunnel's transport (and programs that relay it, such as engarde) is recognised
  and kept apart, so tunnelled traffic is not counted twice and you can see what a tunnel costs on the
  uplinks under it.
- **The router itself**: its own traffic, split by program.

## Names for traffic

wanscope reads, on the LAN interface only, DNS answers and the server name in TLS and QUIC handshakes
(QUIC Initial packets are encrypted with keys anyone can derive; Chrome's split and shuffled ClientHellos
are put back together). A kernel filter passes only those packets to the daemon; bulk traffic never
reaches it. Answers already in AdGuard Home's query log are read at start. About 60 services are known
by name (YouTube, Netflix, Steam, WhatsApp, Windows Update...); you can add your own by domain, address
range or port.

## Control

Per device: pause (15 min, 1 h, until morning, until resumed), block, speed limits, schedules ("weekdays
23:00-07:00 no internet", "weekends 08:00-12:00 limit to 1 MB/s"), and data quotas per day, week or month
that alert, slow down or cut off. New devices can get internet right away, a guest speed, or nothing until
approved. Rules live in their own nftables table (`inet wanscope`), which exists only while a rule applies.

## Alerts

New devices, data plan thresholds (with the pace and run-out date), device quotas, a device using a lot
in a short time, uplinks going down, and a daily report. Sent with a Telegram bot or any command (the
message is its last argument), and kept in a log on the Alerts page.

## History on disk

| Resolution | Kept | Where |
| --- | --- | --- |
| 1 second | last hour (set with `live_seconds`) | memory |
| 1 minute | 365 days (`minute_days`) | `data_dir/min/` |
| 1 hour, with services, sites and programs per device | 2 years (`hour_days`) | `data_dir/hour/` |
| 1 day | for good | `data_dir/day/` |

Files are append-only; a power cut loses at most the minute in progress. A busy home writes about
0.5-1 MB a day. On routers with little flash, point `data_dir` at a USB disk.

## Install

Packages for every OpenWrt target are on the [releases page](../../releases): `.ipk` for OpenWrt 24.10,
`.apk` for 25.12 and newer. Find your architecture with `grep DISTRIB_ARCH /etc/openwrt_release`.

```sh
# OpenWrt 24.10
opkg install ./wanscope_*_<arch>.ipk ./luci-app-wanscope_*_all.ipk
# OpenWrt 25.12 and newer
apk add --allow-untrusted ./wanscope-*_<arch>.apk ./luci-app-wanscope-*.apk
```

Then open **Network → wanscope**. It needs `kmod-nf-conntrack-netlink` (pulled in by the package) and
turns on conntrack byte accounting if it is off. Turn off flow offloading in Network → Firewall: offloaded
connections skip the firewall, so their bytes and the limits are not seen.

## How it counts

- **Uplinks**: `/proc/net/dev` every second, minus the Ethernet header, so the numbers are IP bytes.
- **Devices and services**: conntrack, read every second while a wanscope page is open and every 5 seconds
  otherwise; connections that end in between are caught by destroy events with their final counters. A
  connection's uplink is the interface that owns its NAT address (or mwan3's mark).
- **Devices** are MAC addresses (from the neighbour table and DHCP leases); IPv6 privacy addresses map to
  the same device.

## Configuration

`/etc/config/wanscope` (most of it is on the Settings page):

```
config wanscope 'main'
	option data_dir '/usr/share/wanscope'
	option interval '1'          # conntrack reads while a page is open (s)
	option idle_interval '5'     # and otherwise (s)
	option sniff '1'             # name traffic from DNS / TLS / QUIC
	option minute_days '365'
	option hour_days '731'

config plan 'plan_home'
	option name 'Home SIM'
	option type 'prepaid'        # prepaid | monthly
	option size_gb '60'
	list iface 'wan1'

config device 'd_aabbccddeeff'
	option mac 'aa:bb:cc:dd:ee:ff'
	option name 'Living room TV'
	option icon '📺'
	option down '2000'           # limits in kB/s
	list schedule 'weekdays 23:00-07:00 block'
	list quota 'day total 2048 limit 500 500'
```

## ubus

Everything the pages show is on the `wanscope` ubus object, so scripts can use it too:

```sh
ubus call wanscope status
ubus call wanscope devices
ubus call wanscope plans
ubus call wanscope flows '{"limit":20}'
ubus call wanscope history '{"keys":["i:*"],"from":1700000000,"step":3600}'
```

## Building

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o wanscope ./cmd/wanscope
```

CI builds the daemon for every Go target and packages it with the OpenWrt SDK for each architecture
(the approach of [luci-app-bandix](https://github.com/timsaya/luci-app-bandix)); a release is made when
`PKG_VERSION`/`PKG_RELEASE` in `openwrt/wanscope/Makefile` change. `scripts/deploy.ps1` copies a
development build to a router.

## Credits

Ideas and the CI approach come from [bandix](https://github.com/timsaya/bandix),
[luci-app-bandix](https://github.com/timsaya/luci-app-bandix) and
[luci-app-bandix-plus](https://github.com/timsaya/luci-app-bandix-plus) by timsaya and smallprogram.

## License

Apache 2.0, see [LICENSE](LICENSE).
