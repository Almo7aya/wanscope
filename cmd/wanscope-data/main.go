// Command wanscope-data builds the optional data package's files (openwrt/wanscope-data) on a build machine:
//
//	wanscope-data services <v2fly data dir> <services.txt>   v2fly/domain-list-community (MIT): domains -> services
//	wanscope-data owners <dbip-asn-lite.csv.gz> <owners.bin.gz>  DB-IP ASN Lite (CC BY 4.0): IPv4 ranges -> owners
//
// IPv4 only: wanscope's networks don't route IPv6.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: wanscope-data services <v2fly data dir> <services.txt>\n       wanscope-data owners <dbip-asn-lite.csv.gz> <owners.bin.gz>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "services":
		err = services(os.Args[2], os.Args[3])
	case "owners":
		err = owners(os.Args[2], os.Args[3])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wanscope-data:", err)
		os.Exit(1)
	}
}

// ---------- services ----------

// categories: wanscope's kinds, from the v2fly category lists that include a service (first match wins)
var categories = []struct{ kind, list string }{
	{"games", "category-games-!cn"}, {"games", "category-games"}, {"games", "category-game-platforms-download"},
	{"games", "category-enhance-gaming"},
	{"messaging", "category-communication"}, {"messaging", "category-voip"},
	{"social", "category-social-media-!cn"}, {"social", "category-forums"},
	{"video", "category-media"}, {"video", "category-entertainment"}, {"video", "category-emby"},
	{"cloud", "category-netdisk-!cn"},
	{"work", "category-ai-!cn"}, {"work", "category-ai-chat-!cn"}, {"work", "category-dev"}, {"work", "category-container"},
	{"work", "category-remote-control"}, {"work", "category-password-management"}, {"work", "category-scholar-!cn"},
}

// names v2fly's lowercase list names don't spell the way people do
var names = map[string]string{
	"github": "GitHub", "gitlab": "GitLab", "openai": "OpenAI", "playstation": "PlayStation", "linkedin": "LinkedIn",
	"paypal": "PayPal", "icloud": "iCloud", "tiktok": "TikTok", "whatsapp": "WhatsApp", "youtube": "YouTube",
	"xbox": "Xbox", "ea": "EA", "hbo": "HBO", "bbc": "BBC", "cnn": "CNN", "nvidia": "NVIDIA", "amd": "AMD", "aws": "AWS",
	"ibm": "IBM", "vk": "VK", "x": "X", "ok": "OK", "soundcloud": "SoundCloud", "duckduckgo": "DuckDuckGo",
	"cloudflare": "Cloudflare", "dropbox": "Dropbox", "onedrive": "OneDrive", "macromedia": "Macromedia",
	"teamviewer": "TeamViewer", "anydesk": "AnyDesk", "jetbrains": "JetBrains", "stackexchange": "Stack Exchange",
	"wikimedia": "Wikimedia", "ubisoft": "Ubisoft", "epicgames": "Epic Games", "riot": "Riot Games",
	"blizzard": "Blizzard", "rockstar": "Rockstar Games", "nintendo": "Nintendo", "deepseek": "DeepSeek",
	"perplexity": "Perplexity", "huggingface": "Hugging Face", "speedtest": "Speedtest", "npmjs": "npm",
	"pypi": "PyPI", "docker": "Docker", "visualstudio": "Visual Studio", "bing": "Bing", "msn": "MSN",
}

var lineRe = regexp.MustCompile(`^(?:(domain|full|regexp|keyword):)?([^\s@]+)`)

type list struct {
	domains  []string
	includes []string
}

func readLists(dir string) (map[string]*list, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*list{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		l := &list{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line, _, _ := strings.Cut(sc.Text(), "#")
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if inc, ok := strings.CutPrefix(line, "include:"); ok {
				l.includes = append(l.includes, strings.Fields(inc)[0])
				continue
			}
			m := lineRe.FindStringSubmatch(line)
			if m == nil || m[1] == "regexp" || m[1] == "keyword" {
				continue // patterns a suffix table can't hold
			}
			l.domains = append(l.domains, strings.ToLower(m[2]))
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
		out[e.Name()] = l
	}
	return out, nil
}

func reach(lists map[string]*list, root string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if seen[n] || lists[n] == nil {
			return
		}
		seen[n] = true
		for _, i := range lists[n].includes {
			walk(i)
		}
	}
	walk(root)
	return seen
}

func displayName(id string) string {
	if n, ok := names[id]; ok {
		return n
	}
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if n, ok := names[p]; ok {
			parts[i] = n
		} else if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func services(dir, out string) error {
	lists, err := readLists(dir)
	if err != nil {
		return err
	}
	if len(lists) == 0 {
		return errors.New("no lists in " + dir)
	}
	// China-only lists stay out: reached from geolocation-cn or cn, but not from geolocation-!cn
	cn, notCN := reach(lists, "geolocation-cn"), reach(lists, "geolocation-!cn")
	for k := range reach(lists, "cn") {
		cn[k] = true
	}
	kinds := make([]map[string]bool, len(categories))
	for i, c := range categories {
		kinds[i] = reach(lists, c.list)
	}
	ids := make([]string, 0, len(lists))
	for id, l := range lists {
		switch {
		case len(l.domains) == 0, strings.HasPrefix(id, "category-"), strings.HasPrefix(id, "geolocation-"),
			strings.HasPrefix(id, "tld-"), id == "cn", id == "private", strings.HasSuffix(id, "-cn"),
			cn[id] && !notCN[id]:
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "# wanscope services from v2fly/domain-list-community (MIT, https://github.com/v2fly/domain-list-community), %s\n", time.Now().UTC().Format("2006-01-02"))
	b.WriteString("# Name|kind|domains (each with its subdomains)\n")
	n := 0
	for _, id := range ids {
		kind := "other"
		for i, c := range categories {
			if kinds[i][id] {
				kind = c.kind
				break
			}
		}
		seen := map[string]bool{}
		var ds []string
		for _, d := range lists[id].domains {
			if !seen[d] && strings.Contains(d, ".") {
				seen[d] = true
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s|%s|%s\n", displayName(id), kind, strings.Join(ds, " "))
		n++
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d services -> %s (%d bytes)\n", n, out, b.Len())
	return nil
}

// ---------- owners ----------

// suffixes a company's legal form adds to its name
var legal = regexp.MustCompile(`(?i)[,.]?\s+(inc\.?|llc|l\.l\.c\.|ltd\.?|limited|corp\.?|corporation|co\.,? ltd\.?|gmbh|ag|b\.v\.|bv|s\.a\.|sa|s\.r\.l\.|srl|plc|pte\.? ltd\.?|pty\.? ltd\.?|oy|ab|as|a/s|sas|s\.a\.s\.|kft|sp\. z o\.o\.|jsc|ojsc|llp|lp)$`)

func cleanOwner(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := strings.TrimSpace(legal.ReplaceAllString(s, ""))
		t = strings.TrimRight(t, ",")
		if t == s || t == "" {
			return s
		}
		s = t
	}
}

type rng struct {
	start, end netip.Addr
	owner      string
}

func owners(in, out string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	r := csv.NewReader(gz)
	r.FieldsPerRecord = -1
	var v4 []rng
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(row) < 4 {
			continue
		}
		a, err1 := netip.ParseAddr(row[0])
		z, err2 := netip.ParseAddr(row[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if a.Is4() && z.Is4() {
			v4 = append(v4, rng{a, z, cleanOwner(row[3])})
		}
	}
	if len(v4) == 0 {
		return errors.New("no IPv4 ranges in " + in)
	}
	idx := map[string]uint32{}
	var ownerNames []string
	id := func(s string) uint32 {
		if i, ok := idx[s]; ok {
			return i
		}
		ownerNames = append(ownerNames, s)
		idx[s] = uint32(len(ownerNames)) // 0 is "nobody"
		return idx[s]
	}
	type entry struct{ start, owner uint32 }
	key := func(a netip.Addr) uint32 { b := a.As4(); return binary.BigEndian.Uint32(b[:]) }
	// sorted range starts with their owner: a gap gets an entry of its own (owner 0), neighbours with the same
	// owner merge
	sort.Slice(v4, func(i, j int) bool { return v4[i].start.Less(v4[j].start) })
	var e4 []entry
	var end netip.Addr
	for _, x := range v4 {
		if end.IsValid() && end.Next().IsValid() && end.Next().Less(x.start) {
			e4 = append(e4, entry{key(end.Next()), 0})
		}
		o := id(x.owner)
		if len(e4) == 0 || e4[len(e4)-1].owner != o {
			e4 = append(e4, entry{key(x.start), o})
		}
		if !end.IsValid() || end.Less(x.end) {
			end = x.end
		}
	}
	if end.Next().IsValid() {
		e4 = append(e4, entry{key(end.Next()), 0})
	}

	o, err := os.Create(out)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(o, gzip.BestCompression)
	w := bufio.NewWriter(zw)
	le := binary.LittleEndian
	u32 := func(v uint32) { var b [4]byte; le.PutUint32(b[:], v); w.Write(b[:]) }

	w.WriteString("WSOWN1\n")
	u32(uint32(time.Now().Unix()))
	u32(uint32(len(ownerNames)))
	for _, s := range ownerNames {
		if len(s) > 255 {
			s = s[:255]
		}
		w.WriteByte(byte(len(s)))
		w.WriteString(s)
	}
	u32(uint32(len(e4)))
	for _, e := range e4 {
		u32(e.start)
	}
	for _, e := range e4 {
		u32(e.owner)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	st, _ := os.Stat(out)
	fmt.Printf("%d owners, %d IPv4 ranges -> %s (%d bytes)\n", len(ownerNames), len(e4), out, st.Size())
	return nil
}
