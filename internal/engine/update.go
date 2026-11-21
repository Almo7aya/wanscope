package engine

import (
	"cmp"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almo7aya/wanscope/internal/ubus"
)

// Updates: once a day wanscope asks GitHub for the latest release, and on
// request (or at night, when update_auto is on) installs it with opkg or apk.
// wanscope downloads the packages itself (Go's TLS works where uclient-fetch may not) and checks them
// against GitHub's SHA-256; the install runs as a script outside wanscope, since it restarts wanscope.

const (
	defaultRepo  = "Almo7aya/wanscope"
	updateEvery  = 24 * 3600
	updateRetry  = 3600 // after a failed check
	updateJob    = "/tmp/wanscope-update.json"
	updateScript = "/tmp/wanscope-update.sh"
	updateDir    = "/tmp/wanscope-update.d" // the downloads
)

//go:embed wanscope-update.sh
var updateSh []byte

// updatePkgs are wanscope's packages in install order.
var updatePkgs = []string{"wanscope", "wanscope-data", "luci-app-wanscope"}

type relAsset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest,omitempty"` // sha256:<hex>
}

type release struct {
	Version   string     `json:"version"` // the tag without its v: 0.2.0-r1
	Name      string     `json:"name"`
	Notes     string     `json:"notes"`
	Page      string     `json:"page"`
	Published int64      `json:"published"`
	Assets    []relAsset `json:"assets"` // only this router's
}

func (r *release) asset(name string) *relAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

type updateState struct {
	Checked   int64    `json:"checked"`
	Error     string   `json:"error,omitempty"`
	Release   *release `json:"release,omitempty"`
	Announced string   `json:"announced,omitempty"` // the release an alert told about
	AutoTried string   `json:"auto_tried,omitempty"`
}

type updater struct {
	e     *Engine
	path  string
	fetch sync.Mutex // one check at a time
	mu    sync.Mutex
	st    updateState
	tried atomic.Int64 // the last check, failed or not
}

func newUpdater(e *Engine) *updater {
	u := &updater{e: e, path: filepath.Join(e.Config().DataDir, "update.json")}
	if raw, err := os.ReadFile(u.path); err == nil {
		json.Unmarshal(raw, &u.st)
	}
	return u
}

func (u *updater) save() {
	u.mu.Lock()
	raw, err := json.Marshal(u.st)
	u.mu.Unlock()
	if err == nil && os.WriteFile(u.path+".tmp", raw, 0o644) == nil {
		os.Rename(u.path+".tmp", u.path)
	}
}

// osArch is OpenWrt's package architecture (aarch64_cortex-a72).
func osArch() string {
	raw, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(l, "OPENWRT_ARCH="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// pkgFormat is the package manager's format: apk on OpenWrt 25 and later, ipk before.
func pkgFormat() string {
	if _, err := exec.LookPath("apk"); err == nil {
		return "apk"
	}
	return "ipk"
}

// assetName is a package's file in a release (as CI names them).
func assetName(pkg, ver, arch, format string) string {
	switch {
	case format == "apk" && pkg == "wanscope":
		return pkg + "-" + ver + "_" + arch + ".apk"
	case format == "apk":
		return pkg + "-" + ver + ".apk"
	case pkg == "wanscope":
		return pkg + "_" + ver + "_" + arch + ".ipk"
	}
	return pkg + "_" + ver + "_all.ipk"
}

// installedVersion tells which version of a package is here and how it got
// here: opkg, apk, "dev" (a build copied by hand, scripts/deploy.ps1) or "".
func installedVersion(pkg string) (ver, how string) {
	if pkg == "wanscope" && strings.Contains(Version, "dev") {
		return Version, "dev"
	}
	if raw, err := os.ReadFile("/usr/lib/opkg/info/" + pkg + ".control"); err == nil {
		for _, l := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(l, "Version:"); ok {
				return strings.TrimSpace(v), "opkg"
			}
		}
	}
	if raw, err := os.ReadFile("/lib/apk/db/installed"); err == nil {
		this := false
		for _, l := range strings.Split(string(raw), "\n") {
			switch {
			case l == "":
				this = false
			case strings.HasPrefix(l, "P:"):
				this = l[2:] == pkg
			case this && strings.HasPrefix(l, "V:"):
				return strings.TrimSpace(l[2:]), "apk"
			}
		}
	}
	// copied by hand along with the daemon
	marker := map[string]string{
		"wanscope":          "/usr/bin/wanscope",
		"wanscope-data":     "/usr/share/wanscope/owners.bin.gz",
		"luci-app-wanscope": "/www/luci-static/resources/view/wanscope/overview.js",
	}[pkg]
	if _, err := os.Stat(marker); err == nil {
		return Version, "dev"
	}
	return "", ""
}

// cmpVersion compares versions like 0.2.0-r1. A dev build is older than any
// release of the same version.
func cmpVersion(a, b string) int {
	pa, ra := splitVersion(a)
	pb, rb := splitVersion(b)
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(ra, rb)
}

func splitVersion(v string) (parts []int, rel int) {
	base, suffix, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), "-")
	for _, p := range strings.Split(base, ".") {
		n, _ := strconv.Atoi(p)
		parts = append(parts, n)
	}
	switch n, err := strconv.Atoi(strings.TrimPrefix(suffix, "r")); {
	case suffix == "":
		return parts, 0
	case strings.HasPrefix(suffix, "r") && err == nil:
		return parts, n
	}
	return parts, -1 // dev
}

// check asks GitHub for the latest release (at most once a day unless forced).
func (u *updater) check(force bool) error {
	u.fetch.Lock()
	defer u.fetch.Unlock()
	now := time.Now().Unix()
	u.mu.Lock()
	fresh := now-u.st.Checked < updateEvery && u.st.Error == ""
	u.mu.Unlock()
	if fresh && !force {
		return nil
	}
	u.tried.Store(now)
	r, err := latestRelease(u.e.Config().UpdateRepo)
	u.mu.Lock()
	u.st.Checked = now
	if err != nil {
		u.st.Error = err.Error()
	} else {
		u.st.Error, u.st.Release = "", r
	}
	u.mu.Unlock()
	u.save()
	if err != nil {
		u.e.log.Warn("update check", "err", err)
		return err
	}
	u.announce(r)
	return nil
}

func latestRelease(repo string) (*release, error) {
	if repo == "" || strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("update_repo %q is not owner/name", repo)
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wanscope/"+Version)
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s has no release yet", repo)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var gh struct {
		Tag       string `json:"tag_name"`
		Name      string `json:"name"`
		Body      string `json:"body"`
		Page      string `json:"html_url"`
		Published string `json:"published_at"`
		Assets    []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("GitHub's answer: %w", err)
	}
	r := &release{Version: strings.TrimPrefix(gh.Tag, "v"), Name: gh.Name, Page: gh.Page}
	if len(gh.Body) > 4000 {
		gh.Body = gh.Body[:4000] + "…"
	}
	r.Notes = gh.Body
	if t, err := time.Parse(time.RFC3339, gh.Published); err == nil {
		r.Published = t.Unix()
	}
	arch, format := osArch(), pkgFormat()
	want := map[string]bool{}
	for _, p := range updatePkgs {
		want[assetName(p, r.Version, arch, format)] = true
	}
	for _, a := range gh.Assets {
		if want[a.Name] && strings.HasPrefix(a.URL, "https://") {
			r.Assets = append(r.Assets, relAsset{Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest})
		}
	}
	return r, nil
}

// plan lists what an update would install: the packages that are here, with
// the release's file for each. newer tells whether any of them is older.
func (u *updater) plan(r *release) (rows []map[string]any, files []relAsset, newer bool, err error) {
	arch, format := osArch(), pkgFormat()
	for _, p := range updatePkgs {
		ver, how := installedVersion(p)
		if ver == "" {
			continue
		}
		row := map[string]any{"name": p, "installed": ver, "how": how}
		rows = append(rows, row)
		if r == nil {
			continue
		}
		row["latest"] = r.Version
		a := r.asset(assetName(p, r.Version, arch, format))
		if a == nil {
			if err == nil {
				err = fmt.Errorf("release %s has no %s package for %s (%s)", r.Version, p, arch, format)
			}
			continue
		}
		row["size"] = a.Size
		if cmpVersion(r.Version, ver) > 0 {
			row["update"] = true
			newer = true
		}
		files = append(files, *a)
	}
	return rows, files, newer, err
}

// announce raises an alert the first time a release is newer than this daemon.
func (u *updater) announce(r *release) {
	if cmpVersion(r.Version, Version) <= 0 {
		return
	}
	u.mu.Lock()
	done := u.st.Announced == r.Version
	u.st.Announced = r.Version
	u.mu.Unlock()
	if done {
		return
	}
	u.save()
	n := u.e.Config().Notify
	u.e.alerts.emit("update", r.Version, fmt.Sprintf("⬆️ wanscope %s is out (%s has %s). Update it on wanscope's Settings page.",
		r.Version, u.e.hostname(), Version), n.Updates && n.Method != "none")
}

// tick runs from the slow loop: the daily check, and the night install when asked for.
func (u *updater) tick(now int64) {
	cfg := u.e.Config()
	if !cfg.UpdateCheck || now-u.e.started < 120 {
		return
	}
	u.mu.Lock()
	due := now-u.st.Checked >= updateEvery || u.st.Error != ""
	u.mu.Unlock()
	if due && now-u.tried.Load() >= updateRetry {
		u.tried.Store(now)
		go u.check(false)
		return
	}
	if !cfg.UpdateAuto {
		return
	}
	if h := time.Unix(now, 0).In(u.e.Store.Location()).Hour(); h < 3 || h >= 5 {
		return
	}
	u.mu.Lock()
	r, tried := u.st.Release, u.st.AutoTried
	u.mu.Unlock()
	if r == nil || tried == r.Version {
		return
	}
	if _, _, newer, err := u.plan(r); err != nil || !newer {
		return
	}
	u.mu.Lock()
	u.st.AutoTried = r.Version
	u.mu.Unlock()
	u.save()
	if err := u.start(); err != nil {
		u.e.log.Warn("automatic update", "err", err)
	}
}

// job is the install's progress, written by the script.
func readJob() map[string]any {
	raw, err := os.ReadFile(updateJob)
	if err != nil {
		return nil
	}
	var j map[string]any
	if json.Unmarshal(raw, &j) != nil {
		return nil
	}
	// a script that died (power cut, killed) never says so
	if at, _ := j["at"].(float64); j["state"] == "running" && time.Now().Unix()-int64(at) > 20*60 {
		j["state"], j["message"] = "failed", "The update stopped without finishing."
	}
	return j
}

func writeJob(state, step, msg, ver string) error {
	raw, _ := json.Marshal(map[string]any{"state": state, "step": step, "message": msg, "version": ver, "at": time.Now().Unix()})
	if err := os.WriteFile(updateJob+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(updateJob+".tmp", updateJob)
}

// start launches the install of the latest release.
func (u *updater) start() error {
	if j := readJob(); j != nil && j["state"] == "running" {
		return errors.New("an update is already running")
	}
	u.mu.Lock()
	r := u.st.Release
	u.mu.Unlock()
	if r == nil {
		return errors.New("check for updates first")
	}
	_, assets, newer, err := u.plan(r)
	if err != nil {
		return err
	}
	if !newer {
		return errors.New("wanscope is up to date")
	}
	if err := writeJob("running", "start", "Starting the update", r.Version); err != nil {
		return err
	}
	u.e.log.Info("update started", "version", r.Version, "packages", len(assets))
	go func() {
		if err := install(r.Version, assets); err != nil {
			writeJob("failed", "download", err.Error(), r.Version)
			os.RemoveAll(updateDir)
			u.e.log.Warn("update", "err", err)
		}
	}()
	return nil
}

// install downloads the packages, then hands them to the script.
func install(ver string, assets []relAsset) error {
	os.RemoveAll(updateDir)
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		return err
	}
	var files []string
	for i, a := range assets {
		writeJob("running", "download", fmt.Sprintf("Downloading %s (%d of %d, %s)", a.Name, i+1, len(assets), HumanBytes(uint64(a.Size))), ver)
		p := filepath.Join(updateDir, a.Name)
		if err := download(a, p); err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		files = append(files, p)
	}
	if err := os.WriteFile(updateScript, updateSh, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", append([]string{updateScript, updateJob, ver}, files...)...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func download(a relAsset, path string) error {
	req, err := http.NewRequest("GET", a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wanscope/"+Version)
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("got %d bytes of %d", n, a.Size)
	}
	if want, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && !strings.EqualFold(want, hex.EncodeToString(h.Sum(nil))) {
		return errors.New("the download does not match the release's checksum")
	}
	return nil
}

// brief is for the Overview's notice.
func (u *updater) brief() map[string]any {
	u.mu.Lock()
	r := u.st.Release
	u.mu.Unlock()
	if r == nil {
		return nil
	}
	_, _, newer, err := u.plan(r)
	return map[string]any{"latest": r.Version, "available": newer && err == nil}
}

func (u *updater) report() map[string]any {
	cfg := u.e.Config()
	u.mu.Lock()
	st := u.st
	u.mu.Unlock()
	rows, _, newer, err := u.plan(st.Release)
	out := map[string]any{
		"running": Version, "arch": osArch(), "format": pkgFormat(), "repo": cfg.UpdateRepo,
		"check": cfg.UpdateCheck, "auto": cfg.UpdateAuto, "checked": st.Checked, "packages": rows,
		"available": newer && err == nil,
	}
	if st.Error != "" {
		out["error"] = st.Error
	} else if err != nil && newer {
		out["error"] = err.Error()
	}
	if r := st.Release; r != nil {
		out["latest"] = r.Version
		out["name"], out["notes"], out["page"], out["published"] = r.Name, r.Notes, r.Page, r.Published
	}
	if j := readJob(); j != nil {
		out["job"] = j
	}
	return out
}

func (e *Engine) apiUpdate(r *ubus.Request) (any, error) {
	var q struct {
		Check bool `json:"check"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if q.Check {
		e.updates.check(true) // the error is in the report
	}
	return e.updates.report(), nil
}

func (e *Engine) apiUpdateStart(*ubus.Request) (any, error) {
	if err := e.updates.start(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}
