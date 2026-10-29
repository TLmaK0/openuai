package skills

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Listing limits (Claude Code's defaults): each entry's description plus
// when_to_use is cut at DefaultMaxDescChars, and the whole listing has a
// budget of about 1% of a 200k-token context window.
const (
	DefaultMaxDescChars  = 1536
	DefaultListingBudget = 8000
)

// ListOptions controls the skill listing the assistant sees.
type ListOptions struct {
	MaxDescChars int // 0: DefaultMaxDescChars
	Budget       int // characters for the whole listing; 0: DefaultListingBudget
	// Usage counts invocations per skill: when the listing overflows, the
	// least used skills lose their descriptions first.
	Usage map[string]int
	// PathActive reports whether a skill with paths matches a file the
	// assistant has worked on; skills with paths are listed only then.
	PathActive func(Skill) bool
}

// Listed reports whether the assistant sees the skill in its listing.
func (sk Skill) Listed(pathActive func(Skill) bool) bool {
	if !sk.ModelInvocable() {
		return false
	}
	if len(sk.Paths) > 0 {
		return pathActive != nil && pathActive(sk)
	}
	return true
}

var angle = strings.NewReplacer("<", "&lt;", ">", "&gt;")

// Describe lists the skills the assistant may load on its own with the
// default limits (no path-scoped skills).
func (s *Set) Describe() string {
	l, _ := s.Listing(ListOptions{})
	return l
}

// Listing returns the skill listing for the skill tool and whether it had
// to drop descriptions to fit the budget. Every listed skill keeps its name.
func (s *Set) Listing(o ListOptions) (string, bool) {
	if o.MaxDescChars <= 0 {
		o.MaxDescChars = DefaultMaxDescChars
	}
	if o.Budget <= 0 {
		o.Budget = DefaultListingBudget
	}
	type entry struct {
		name, desc string
		uses       int
	}
	var entries []*entry
	qualified := false
	for _, sk := range s.Skills {
		if !sk.Listed(o.PathActive) {
			continue
		}
		e := &entry{name: sk.Name, uses: o.Usage[sk.Name]}
		if sk.Visibility != VisibilityNameOnly {
			d := sk.Description
			if sk.WhenToUse != "" {
				d += " " + sk.WhenToUse
			}
			if r := []rune(d); len(r) > o.MaxDescChars {
				d = string(r[:o.MaxDescChars]) + "…"
			}
			e.desc = angle.Replace(strings.Join(strings.Fields(d), " "))
		}
		if sk.Scope != "" && strings.Contains(sk.Name, ":") {
			qualified = true
		}
		entries = append(entries, e)
	}
	size := func() int {
		n := 0
		for _, e := range entries {
			n += len(e.name) + 3
			if e.desc != "" {
				n += len(e.desc) + 2
			}
		}
		return n
	}
	overflow := false
	if size() > o.Budget {
		overflow = true
		byUse := append([]*entry(nil), entries...)
		sort.SliceStable(byUse, func(i, j int) bool { return byUse[i].uses < byUse[j].uses })
		for _, e := range byUse {
			if size() <= o.Budget {
				break
			}
			e.desc = ""
		}
	}
	var b strings.Builder
	for _, e := range entries {
		if e.desc != "" {
			fmt.Fprintf(&b, "- %s: %s\n", e.name, e.desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", e.name)
		}
	}
	if qualified {
		b.WriteString("Some skills share a name: invoke the one whose directory holds the files you are working on (dir:name).\n")
	}
	return b.String(), overflow
}

// MatchesPath reports whether rel (a path relative to the project, with /)
// matches one of the skill's paths patterns.
func (sk Skill) MatchesPath(rel string) bool {
	for _, p := range sk.Paths {
		if GlobMatch(p, rel) {
			return true
		}
	}
	return false
}

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// GlobMatch matches a relative path against a glob: ** crosses directories,
// * and ? stay within one, {a,b} picks alternatives and [..] classes work.
// A pattern without / also matches the file name in any directory.
func GlobMatch(pattern, rel string) bool {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	rel = strings.TrimPrefix(rel, "./")
	if pattern == "" || rel == "" || strings.HasPrefix(rel, "../") {
		return false
	}
	re := globRegexp(pattern)
	if re == nil {
		return false
	}
	if re.MatchString(rel) {
		return true
	}
	return !strings.Contains(pattern, "/") && re.MatchString(path.Base(rel))
}

func globRegexp(p string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[p]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	depth := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				i++
				if i+1 < len(p) && p[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			depth++
			b.WriteString("(?:")
		case '}':
			if depth > 0 {
				depth--
				b.WriteString(")")
			} else {
				b.WriteString(`\}`)
			}
		case ',':
			if depth > 0 {
				b.WriteString("|")
			} else {
				b.WriteString(",")
			}
		case '[':
			if j := strings.IndexByte(p[i:], ']'); j > 1 {
				class := p[i+1 : i+j]
				if strings.HasPrefix(class, "!") {
					class = "^" + class[1:]
				}
				b.WriteString("[" + class + "]")
				i += j
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	for ; depth > 0; depth-- {
		b.WriteString(")")
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		re = nil
	}
	if len(globCache) < 1000 {
		globCache[p] = re
	}
	return re
}
