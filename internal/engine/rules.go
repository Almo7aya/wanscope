package engine

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/Almo7aya/wanscope/internal/ubus"
	"github.com/Almo7aya/wanscope/internal/uci"
)

var (
	ruleDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	rulePortRe   = regexp.MustCompile(`^(tcp|udp):\d{1,5}(-\d{1,5})?$`)
	ruleKinds    = map[string]bool{"video": true, "games": true, "social": true, "messaging": true, "music": true,
		"cloud": true, "updates": true, "work": true, "other": true}
)

// apiSetRule names a service (the Activity page's "Name it"): one match, a domain with its subdomains, an IPv4
// address or range, or a port, joins your rule of that name, or starts one. Live connections take the name at
// once (Reload labels them again).
func (e *Engine) apiSetRule(r *ubus.Request) (any, error) {
	var q struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Domain   string `json:"domain"`
		CIDR     string `json:"cidr"`
		Port     string `json:"port"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 64 || strings.ContainsAny(q.Name, "|\n") {
		return nil, ubus.Errorf("give the service a name (up to 64 characters)")
	}
	if !ruleKinds[q.Category] {
		q.Category = "other"
	}
	var list, value string
	n := 0
	if d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(q.Domain)), "*."); d != "" {
		if !ruleDomainRe.MatchString(d) {
			return nil, ubus.Errorf("%q isn't a domain", q.Domain)
		}
		list, value = "domain", d
		n++
	}
	if c := strings.TrimSpace(q.CIDR); c != "" {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, err2 := netip.ParseAddr(c)
			if err2 != nil {
				return nil, ubus.Errorf("%q isn't an address or range", c)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if !p.Addr().Is4() {
			return nil, ubus.Errorf("only IPv4 addresses")
		}
		list, value = "cidr", p.Masked().String()
		n++
	}
	if pt := strings.ToLower(strings.TrimSpace(q.Port)); pt != "" {
		if !rulePortRe.MatchString(pt) {
			return nil, ubus.Errorf("a port is tcp:PORT or udp:FROM-TO")
		}
		list, value = "port", pt
		n++
	}
	if n != 1 {
		return nil, ubus.Errorf("give one domain, address or port")
	}

	// your rule of that name gets the match; else a new one
	sec := ""
	for _, c := range e.Config().Rules {
		if strings.EqualFold(c.Name, q.Name) {
			sec = c.Section
			for _, have := range map[string][]string{"domain": c.Domains, "cidr": c.CIDRs, "port": c.Ports}[list] {
				if have == value {
					return map[string]any{"ok": true, "section": sec}, nil // there already
				}
			}
			break
		}
	}
	var cmds []string
	if sec == "" {
		base := "rule_" + planIDRe.ReplaceAllString(strings.ToLower(q.Name), "_")
		sec = base
		for i := 2; e.ruleSectionTaken(sec); i++ {
			sec = fmt.Sprintf("%s_%d", base, i)
		}
		cmds = append(cmds, "set "+ConfigName+"."+sec+"=rule", "set "+ConfigName+"."+sec+".name="+uci.Quote(q.Name))
	}
	cmds = append(cmds, "set "+ConfigName+"."+sec+".category="+uci.Quote(q.Category),
		"add_list "+ConfigName+"."+sec+"."+list+"="+uci.Quote(value))
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	e.log.Info("service named", "name", q.Name, list, value)
	return map[string]any{"ok": true, "section": sec}, e.Reload()
}

func (e *Engine) ruleSectionTaken(sec string) bool {
	for _, c := range e.Config().Rules {
		if c.Section == sec {
			return true
		}
	}
	return false
}
