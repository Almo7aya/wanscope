# wanscope

A bandwidth monitor for OpenWrt routers that know more than one way out: several WANs or SIMs, VPN tunnels,
load balancing. It shows, live and over time, **which device used which uplink for what**, how much data is
left on each data plan, and it can limit, schedule, cap or block devices.

wanscope is a small Go daemon with LuCI pages. It reads what the router already knows (conntrack and the
interface counters) instead of counting packets on one interface, so a device's traffic is counted once
with the uplink it actually left through, even on one-armed routers and with mwan3.

## What it shows

- **Overview**: download and upload right now, stacked by uplink, second by second; the share of each
  uplink; the devices moving data now and what each is using; what is left of each data plan; what used
  the internet today.
- **Activity**: what every device is doing right now, by service: speed, connections, servers, uplink and
  protocol (QUIC, TCP, UDP), down to each connection (server name and the CNAME it went through, address
  and ports, how it was named, who owns the address, the program or tunnel, speed and data). **Name it**
  turns traffic nothing names into a service of your own.
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
reaches it. Answers already in AdGuard Home's query log are read at start.

A connection's service comes from, first to last: your own rules (by domain, IPv4 address range or port;
"Name it" on the Activity page adds them), the built-in list (about 65 services: YouTube, Netflix, Steam,
WhatsApp, Windows Update, the anti-cheats...), the optional `wanscope-data` lists, its site's name, a port
that says what it is (DNS, mail, SSH...), and last who owns the server's address ("Amazon.com network").

The optional **wanscope-data** package (about 3 MB, the same for every router) brings two lists, made from
the latest sources when a release is built:
- about 1,100 more services by domain, from [v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)
  (MIT), without the China-only lists;
- who announces every IPv4 range, from DB-IP's ASN Lite ([IP Geolocation by DB-IP](https://db-ip.com), CC BY 4.0).
  IPv4 only.

## Control

Per device: pause (15 min, 1 h, until morning, until resumed), block, speed limits, schedules ("weekdays
23:00-07:00 no internet", "weekends 08:00-12:00 limit to 1 MB/s"), and data quotas per day, week or month
that alert, slow down or cut off. New devices can get internet right away, a guest speed, or nothing until
approved. Rules live in their own nftables table (`inet wanscope`), which exists only while a rule applies.

Services can be blocked too, on one device or on every device (**Block** on the Activity page, or
`ubus call wanscope block_service '{"service":"YouTube","device":"aa:bb:cc:dd:ee:ff","block":true}'`; no
device means every device). It is IP blocking: an nftables set per blocked service holds the IPv4 addresses
its connections use and the DNS answers that name it, and traffic to them is dropped; nothing is filtered in
DNS. An address stays blocked until 6 hours after it was last seen. Big services share servers (CDNs), so
whatever else uses the same addresses stops too.

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

Optionally add the data package (above):

```sh
opkg install ./wanscope-data_*_all.ipk           # OpenWrt 24.10
apk add --allow-untrusted ./wanscope-data-*.apk   # OpenWrt 25.12 and newer
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
	list block_service 'TikTok'  # its addresses, on this device

config wanscope 'main'
	list block_service 'Roblox'  # on every device
```

## ubus

Everything the pages show is on the `wanscope` ubus object, so scripts can use it too:

```sh
ubus call wanscope status
ubus call wanscope devices
ubus call wanscope plans
ubus call wanscope flows '{"limit":20}'
ubus call wanscope history '{"keys":["i:*"],"from":1700000000,"step":3600}'
ubus call wanscope set_rule '{"name":"Denuvo Anti-Cheat","category":"games","domain":"codefusion.technology"}'
```

## Building

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o wanscope ./cmd/wanscope
```

CI builds the daemon for every Go target and packages it with the OpenWrt SDK for each architecture
(the approach of [luci-app-bandix](https://github.com/timsaya/luci-app-bandix)); a release is made when
`PKG_VERSION`/`PKG_RELEASE` in `openwrt/wanscope/Makefile` change. `scripts/deploy.ps1` copies a
development build to a router.

The data package's lists are made with `cmd/wanscope-data` (CI does this for each release):

```sh
git clone --depth 1 https://github.com/v2fly/domain-list-community /tmp/dlc
curl -fsSLo /tmp/asn.csv.gz "https://download.db-ip.com/free/dbip-asn-lite-$(date -u +%Y-%m).csv.gz"
go run ./cmd/wanscope-data services /tmp/dlc/data openwrt/wanscope-data/data/services.txt
go run ./cmd/wanscope-data owners /tmp/asn.csv.gz openwrt/wanscope-data/data/owners.bin.gz
```

## Credits

Ideas and the CI approach come from [bandix](https://github.com/timsaya/bandix),
[luci-app-bandix](https://github.com/timsaya/luci-app-bandix) and
[luci-app-bandix-plus](https://github.com/timsaya/luci-app-bandix-plus) by timsaya and smallprogram.

The data package's lists come from [v2fly/domain-list-community](https://github.com/v2fly/domain-list-community)
(MIT) and [IP Geolocation by DB-IP](https://db-ip.com) (DB-IP's ASN Lite, CC BY 4.0).

## License

Apache 2.0, see [LICENSE](LICENSE).
