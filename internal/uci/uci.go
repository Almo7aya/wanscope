// Package uci reads OpenWrt UCI files and changes them through the uci CLI,
// so other tools (LuCI, uci) see consistent state.
package uci

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Section struct {
	Type    string
	Name    string // empty for anonymous sections
	Index   int    // position among sections of the same type
	Options map[string]string
	Lists   map[string][]string
}

func (s *Section) Get(opt, def string) string {
	if v, ok := s.Options[opt]; ok {
		return v
	}
	return def
}

func (s *Section) Bool(opt string, def bool) bool {
	v, ok := s.Options[opt]
	if !ok {
		return def
	}
	switch v {
	case "1", "on", "yes", "true", "enabled":
		return true
	}
	return false
}

// List returns a list option, or the option split on spaces when it was
// given as a plain option.
func (s *Section) List(opt string) []string {
	if l, ok := s.Lists[opt]; ok {
		return l
	}
	if v, ok := s.Options[opt]; ok && v != "" {
		return strings.Fields(v)
	}
	return nil
}

type File struct{ Sections []*Section }

func (f *File) ByName(name string) *Section {
	for _, s := range f.Sections {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (f *File) ByType(typ string) []*Section {
	var out []*Section
	for _, s := range f.Sections {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

// Load reads /etc/config/<name> (or a path). A missing file is empty.
func Load(name string) (*File, error) {
	path := name
	if !strings.Contains(name, "/") {
		path = "/etc/config/" + name
	}
	fh, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, err
	}
	defer fh.Close()
	f := &File{}
	var cur *Section
	counts := map[string]int{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		words := split(sc.Text())
		if len(words) == 0 {
			continue
		}
		switch words[0] {
		case "config":
			if len(words) < 2 {
				continue
			}
			cur = &Section{Type: words[1], Options: map[string]string{}, Lists: map[string][]string{}, Index: counts[words[1]]}
			counts[words[1]]++
			if len(words) > 2 {
				cur.Name = words[2]
			}
			f.Sections = append(f.Sections, cur)
		case "option":
			if cur != nil && len(words) >= 2 {
				v := ""
				if len(words) > 2 {
					v = words[2]
				}
				cur.Options[words[1]] = v
			}
		case "list":
			if cur != nil && len(words) >= 3 {
				cur.Lists[words[1]] = append(cur.Lists[words[1]], words[2])
			}
		}
	}
	return f, sc.Err()
}

// split breaks a UCI line into words, honouring quotes and comments.
func split(line string) []string {
	var out []string
	var b strings.Builder
	inWord, quote := false, byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '#' && !inWord:
			return out
		case c == ' ' || c == '\t':
			if inWord {
				out = append(out, b.String())
				b.Reset()
				inWord = false
			}
		case c == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
			inWord = true
		default:
			b.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, b.String())
	}
	return out
}

// Quote returns v as a single-quoted uci batch value.
func Quote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// Batch runs uci commands (one per line, like "set wanscope.x.name='y'")
// and commits the config.
func Batch(config string, cmds []string) error {
	if len(cmds) == 0 {
		return nil
	}
	script := strings.Join(cmds, "\n") + "\ncommit " + config + "\n"
	cmd := exec.Command("uci", "-q", "batch")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uci batch: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
