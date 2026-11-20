package names

import (
	"net/netip"
	"strconv"
	"strings"
)

// Service is what a connection is for, in words a person uses.
type Service struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

// builtin lists services by the domains they use: "Name|category|domains".
var builtin = []string{
	"YouTube|video|youtube.com googlevideo.com ytimg.com youtu.be youtube-nocookie.com youtubei.googleapis.com yt3.ggpht.com",
	"Netflix|video|netflix.com nflxvideo.net nflximg.net nflxext.com nflxso.net netflix.net",
	"Prime Video|video|primevideo.com aiv-cdn.net aiv-delivery.net pv-cdn.net media-amazon.com",
	"Disney+|video|disneyplus.com disney-plus.net dssott.com bamgrid.com",
	"Shahid|video|shahid.net shahid.mbc.net mbc.net",
	"OSN+|video|osnplus.com osn.com",
	"Twitch|video|twitch.tv ttvnw.net jtvnw.net twitchcdn.net",
	"TikTok|social|tiktok.com tiktokcdn.com tiktokv.com ttwstatic.com byteoversea.com ibytedtos.com ibyteimg.com tiktokcdn-us.com muscdn.com bytefcdn-oversea.com",
	"Instagram|social|instagram.com cdninstagram.com",
	"Facebook|social|facebook.com fbcdn.net facebook.net fb.com fbsbx.com",
	"Snapchat|social|snapchat.com snapkit.co sc-cdn.net snap-dev.net feelinsonice-hrd.appspot.com sc-static.net",
	"X (Twitter)|social|twitter.com x.com twimg.com t.co",
	"Reddit|social|reddit.com redd.it redditmedia.com redditstatic.com",
	"Pinterest|social|pinterest.com pinimg.com",
	"WhatsApp|messaging|whatsapp.com whatsapp.net wa.me",
	"Telegram|messaging|telegram.org t.me telegram.me telesco.pe tdesktop.com",
	"Discord|messaging|discord.com discord.gg discordapp.com discordapp.net discord.media discordcdn.com",
	"Signal|messaging|signal.org whispersystems.org",
	"Zoom|work|zoom.us zoom.com zoomgov.com",
	"Microsoft Teams|work|teams.microsoft.com teams.live.com skype.com lync.com",
	"Slack|work|slack.com slack-edge.com slack-msgs.com",
	"Spotify|music|spotify.com scdn.co spotifycdn.com spotify.design",
	"Anghami|music|anghami.com anghcdn.co anghamiwebcdn.com",
	"Apple Music|music|music.apple.com itunes.apple.com",
	"SoundCloud|music|soundcloud.com sndcdn.com",
	"Steam|games|steampowered.com steamcommunity.com steamcontent.com steamserver.net steamstatic.com steam-chat.com valvesoftware.com",
	"Epic Games|games|epicgames.com epicgames.dev unrealengine.com fortnite.com",
	"THE FINALS|games|embark.games embark-studios.com reachthefinals.com",
	"Riot Games|games|riotgames.com riotcdn.net leagueoflegends.com playvalorant.com",
	"Battle.net|games|battle.net blizzard.com blzstatic.com",
	"EA|games|ea.com origin.com eaassets-a.akamaihd.net",
	"Xbox|games|xbox.com xboxlive.com xboxservices.com",
	"PlayStation|games|playstation.com playstation.net sonyentertainmentnetwork.com",
	"Nintendo|games|nintendo.com nintendo.net",
	"Roblox|games|roblox.com rbxcdn.com",
	"Minecraft|games|minecraft.net minecraftservices.com mojang.com",
	"Windows Update|updates|windowsupdate.com update.microsoft.com delivery.mp.microsoft.com dl.delivery.mp.microsoft.com do.dsp.mp.microsoft.com",
	"Microsoft|cloud|microsoft.com office.com office.net live.com msftconnecttest.com msn.com bing.com microsoftonline.com outlook.com sharepoint.com onedrive.com azureedge.net msedge.net",
	"Apple|cloud|apple.com icloud.com mzstatic.com aaplimg.com apple-cloudkit.com cdn-apple.com icloud-content.com apple.news",
	"Google|web|google.com googleapis.com gstatic.com gvt1.com gvt2.com googleusercontent.com 1e100.net ggpht.com google.co google.com.sa withgoogle.com",
	"Google Drive|cloud|drive.google.com docs.google.com",
	"Gmail|work|mail.google.com gmail.com",
	"Android|updates|android.com play.googleapis.com android.clients.google.com",
	"Amazon|shopping|amazon.com amazon.sa amazon.ae ssl-images-amazon.com",
	"AWS|cloud|amazonaws.com aws.amazon.com cloudfront.net",
	"Cloudflare|cloud|cloudflare.com cloudflare-dns.com one.one.one.one",
	"Akamai|cloud|akamai.net akamaized.net akamaihd.net akamaiedge.net edgekey.net edgesuite.net",
	"GitHub|work|github.com githubusercontent.com githubassets.com",
	"ChatGPT|work|openai.com chatgpt.com oaiusercontent.com",
	"Claude|work|anthropic.com claude.ai",
	"Speedtest|web|speedtest.net ookla.com fast.com",
	"Tailscale|cloud|tailscale.com tailscale.io",
	"Ads & tracking|ads|doubleclick.net googlesyndication.com googleadservices.com google-analytics.com app-measurement.com adservice.google.com appsflyer.com adjust.com branch.io scorecardresearch.com criteo.com",
}

var builtinCIDRs = []string{
	"THE FINALS|games|202.14.113.0/24",
	"Telegram|messaging|91.108.4.0/22 91.108.8.0/22 91.108.12.0/22 91.108.16.0/22 91.108.56.0/22 149.154.160.0/20 2001:67c:4e8::/48 2001:b28:f23c::/46",
	"Meta|social|157.240.0.0/16 31.13.64.0/18 129.134.0.0/16",
}

// common second-level suffixes under which a site is three labels long
var secondLevel = map[string]bool{}

func init() {
	for _, s := range strings.Fields(`co.uk org.uk ac.uk gov.uk com.au net.au org.au co.jp ne.jp co.kr co.in co.nz co.za
		com.sa net.sa org.sa gov.sa edu.sa com.ae net.ae gov.ae com.eg com.tr com.br com.cn com.hk com.tw com.sg com.my
		com.pk com.ye net.ye com.om com.qa com.kw com.bh com.jo com.lb com.iq com.mx com.ar co.id or.id com.ph
		github.io appspot.com herokuapp.com blogspot.com`) {
		secondLevel[s] = true
	}
}

// Site is a host's registrable domain (approximately: no public suffix list).
func Site(host string) string {
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	host = strings.TrimSuffix(host, ".")
	parts := strings.Split(host, ".")
	n := len(parts)
	if n <= 2 {
		return host
	}
	if secondLevel[parts[n-2]+"."+parts[n-1]] {
		return strings.Join(parts[n-3:], ".")
	}
	return parts[n-2] + "." + parts[n-1]
}

type cidrRule struct {
	prefix netip.Prefix
	svc    *Service
}

type portRule struct {
	proto    uint8
	from, to uint16
	svc      *Service
}

// Rules classify connections into services.
type Rules struct {
	suffix map[string]*Service
	cidrs  []cidrRule
	ports  []portRule
}

// CustomRule is a user's rule from the config.
type CustomRule struct {
	Name, Category string
	Domains        []string
	CIDRs          []string
	Ports          []string // "udp:7000-7999", "tcp:25565"
}

func NewRules(custom []CustomRule) *Rules {
	r := &Rules{suffix: map[string]*Service{}}
	// custom rules first: they win over the built-in ones
	for _, c := range custom {
		svc := &Service{Name: c.Name, Category: c.Category}
		if svc.Category == "" {
			svc.Category = "other"
		}
		for _, d := range c.Domains {
			d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "*."))
			if d != "" {
				if _, ok := r.suffix[d]; !ok {
					r.suffix[d] = svc
				}
			}
		}
		for _, p := range c.CIDRs {
			if pf, err := netip.ParsePrefix(strings.TrimSpace(p)); err == nil {
				r.cidrs = append(r.cidrs, cidrRule{pf.Masked(), svc})
			} else if a, err := netip.ParseAddr(strings.TrimSpace(p)); err == nil {
				r.cidrs = append(r.cidrs, cidrRule{netip.PrefixFrom(a, a.BitLen()), svc})
			}
		}
		for _, p := range c.Ports {
			if pr, ok := parsePort(p, svc); ok {
				r.ports = append(r.ports, pr)
			}
		}
	}
	for _, line := range builtin {
		f := strings.SplitN(line, "|", 3)
		svc := &Service{Name: f[0], Category: f[1]}
		for _, d := range strings.Fields(f[2]) {
			if _, ok := r.suffix[d]; !ok {
				r.suffix[d] = svc
			}
		}
	}
	for _, line := range builtinCIDRs {
		f := strings.SplitN(line, "|", 3)
		svc := &Service{Name: f[0], Category: f[1]}
		for _, p := range strings.Fields(f[2]) {
			if pf, err := netip.ParsePrefix(p); err == nil {
				r.cidrs = append(r.cidrs, cidrRule{pf, svc})
			}
		}
	}
	return r
}

func parsePort(s string, svc *Service) (portRule, bool) {
	proto, rng, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), ":")
	if !ok {
		return portRule{}, false
	}
	pr := portRule{svc: svc}
	switch proto {
	case "tcp":
		pr.proto = 6
	case "udp":
		pr.proto = 17
	default:
		return portRule{}, false
	}
	a, b, isRange := strings.Cut(rng, "-")
	from, err := strconv.Atoi(a)
	if err != nil {
		return portRule{}, false
	}
	to := from
	if isRange {
		if to, err = strconv.Atoi(b); err != nil {
			return portRule{}, false
		}
	}
	pr.from, pr.to = uint16(from), uint16(to)
	return pr, true
}

func (r *Rules) byHost(host string) *Service {
	for h := host; h != ""; {
		if s, ok := r.suffix[h]; ok {
			return s
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return nil
}

// Classify finds the service of a connection to remote (proto, port) whose
// server name is host (with alias, a CNAME it resolved through).
func (r *Rules) Classify(host, alias string, remote netip.Addr, proto uint8, port uint16) *Service {
	for _, pr := range r.ports {
		if pr.proto == proto && port >= pr.from && port <= pr.to {
			return pr.svc
		}
	}
	// a name says more than an address range (one provider's range serves
	// several of its apps)
	if host != "" {
		if s := r.byHost(host); s != nil {
			return s
		}
	}
	if alias != "" {
		if s := r.byHost(alias); s != nil {
			return s
		}
	}
	for _, c := range r.cidrs {
		if c.prefix.Contains(remote) {
			return c.svc
		}
	}
	return nil
}

// PortName names well-known ports for connections without a host name.
func PortName(proto uint8, port uint16) string {
	switch {
	case port == 53 || port == 853:
		return "DNS"
	case proto == 17 && port == 123:
		return "NTP"
	case proto == 17 && port == 443:
		return "QUIC"
	case proto == 6 && port == 443:
		return "HTTPS"
	case proto == 6 && port == 80:
		return "HTTP"
	case proto == 6 && (port == 993 || port == 143 || port == 587 || port == 465 || port == 25 || port == 995):
		return "Mail"
	case proto == 6 && port == 22:
		return "SSH"
	case proto == 17 && (port == 51820 || port == 41641):
		return "WireGuard"
	case proto == 17 && port >= 3478 && port <= 3481:
		return "Calls (STUN)"
	case proto == 1 || proto == 58:
		return "Ping"
	}
	return ""
}

// Categories maps every known service name to its category.
func Categories(r *Rules) map[string]string {
	m := map[string]string{}
	for _, s := range r.suffix {
		m[s.Name] = s.Category
	}
	for _, c := range r.cidrs {
		m[c.svc.Name] = c.svc.Category
	}
	for _, p := range r.ports {
		m[p.svc.Name] = p.svc.Category
	}
	return m
}
