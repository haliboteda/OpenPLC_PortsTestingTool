package ptpanel

// The panel dictionary is whole and current (decision 82, PORTTOOL-FLOW.md
// C.4.1 in $PROD; decided in LANG-07 and LANG-08 of
// $PROD/maps/porttool-panel-languages/):
//  1. every entry has zh, en and de, none empty, with the same {placeholders};
//  2. en_of / de_of are the hash of the Chinese each translation was made from;
//  3. every key the page and the Go code use is in the dictionary, and every
//     dictionary key is used;
//  4. no Chinese outside the dictionary in the page or the Go code feeding it,
//     except what strings.json lists under not_translated.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

var (
	// The Go packages whose literals reach the panel.
	stringsGoDirs = []string{".", "../simboard", "../ptecho", "../ptitems", "../refboard", "../calstore"}
	cjkRe         = regexp.MustCompile(`[\x{4e00}-\x{9fff}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]`)
	placeholderRe = regexp.MustCompile(`\{([a-z0-9_]+)\}`)
)

type literal struct {
	file string
	line int
	text string
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func lineAt(src []rune, i int) int {
	n := 1
	for _, r := range src[:i] {
		if r == '\n' {
			n++
		}
	}
	return n
}

func hasPrefixAt(src []rune, i int, p string) bool {
	return strings.HasPrefix(string(src[i:min(len(src), i+len(p))]), p)
}

func indexFrom(src []rune, i int, p string) int {
	j := strings.Index(string(src[i:]), p)
	if j < 0 {
		return -1
	}
	return i + len([]rune(string(src[i:])[:j]))
}

// jsStrings returns every string and template literal, skipping comments and regexes.
func jsStrings(file, s string) []literal {
	src := []rune(s)
	var out []literal
	prev := ""
	for i, n := 0, len(src); i < n; {
		c := src[i]
		if hasPrefixAt(src, i, "//") {
			if j := indexFrom(src, i, "\n"); j >= 0 {
				i = j
			} else {
				i = n
			}
			continue
		}
		if hasPrefixAt(src, i, "/*") {
			i = indexFrom(src, i, "*/") + 2
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			j := i + 1
			for src[j] != c {
				if src[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			out = append(out, literal{file, lineAt(src, i), string(src[i+1 : j])})
			prev, i = "x", j+1
			continue
		}
		if c == '/' && (prev == "" || strings.ContainsAny(prev, "(,=:[!&|?{};+-*%<>~^")) {
			j, class := i+1, false
			for {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '[' {
					class = true
				} else if src[j] == ']' {
					class = false
				} else if src[j] == '/' && !class {
					break
				}
				j++
			}
			prev, i = "x", j+1
			continue
		}
		if !unicode.IsSpace(c) {
			prev = string(c)
		}
		i++
	}
	return out
}

// goStrings returns every Go string or rune literal, skipping comments.
func goStrings(file, s string) []literal {
	src := []rune(s)
	var out []literal
	for i, n := 0, len(src); i < n; {
		if hasPrefixAt(src, i, "//") {
			if j := indexFrom(src, i, "\n"); j >= 0 {
				i = j
			} else {
				i = n
			}
			continue
		}
		if hasPrefixAt(src, i, "/*") {
			i = indexFrom(src, i, "*/") + 2
			continue
		}
		c := src[i]
		if c == '"' || c == '`' || c == '\'' {
			j := i + 1
			for src[j] != c {
				if src[j] == '\\' && c != '`' {
					j += 2
				} else {
					j++
				}
			}
			out = append(out, literal{file, lineAt(src, i), string(src[i+1 : j])})
			i = j + 1
			continue
		}
		i++
	}
	return out
}

var (
	maskRe    = regexp.MustCompile(`(?s)<script>.*?</script>|<style>.*?</style>|<!--.*?-->`)
	markupRe  = regexp.MustCompile(`>([^<>]*)<|"([^"]*)"`)
	scriptRe  = regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	attrKeyRe = regexp.MustCompile(`data-t(?:-placeholder)?="([^"]+)"`)
	jsCallRe  = regexp.MustCompile(`\bT\("([^"]+)"`)
	goCallRe  = regexp.MustCompile(`\bm\("([^"]+)"`)
)

// htmlText returns markup text and attributes outside script, style and comments.
func htmlText(page string) []literal {
	masked := maskRe.ReplaceAllStringFunc(page, func(m string) string {
		return strings.Repeat("\n", strings.Count(m, "\n"))
	})
	var out []literal
	for no, line := range strings.Split(masked, "\n") {
		for _, m := range markupRe.FindAllStringSubmatch(line, -1) {
			t := m[1]
			if t == "" {
				t = m[2]
			}
			out = append(out, literal{"index.html", no + 1, t})
		}
	}
	return out
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDictionaryIsWholeAndCurrent(t *testing.T) {
	var doc struct {
		Strings       map[string]map[string]any     `json:"strings"`
		NotTranslated []struct{ File, Text string } `json:"not_translated"`
	}
	if err := json.Unmarshal([]byte(readText(t, "web/strings.json")), &doc); err != nil {
		t.Fatal(err)
	}
	words := doc.Strings
	str := func(e map[string]any, k string) string { s, _ := e[k].(string); return s }
	var problems []string
	add := func(s string) { problems = append(problems, s) }

	keys := make([]string, 0, len(words))
	for k := range words {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	phs := func(s string) string {
		var p []string
		for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
			p = append(p, m[1])
		}
		sort.Strings(p)
		return strings.Join(p, ",")
	}
	for _, key := range keys {
		e := words[key]
		for _, lang := range []string{"zh", "en", "de"} {
			if strings.TrimSpace(str(e, lang)) == "" {
				add(key + ": no " + lang + " text")
			}
		}
		for _, lang := range []string{"en", "de"} {
			if str(e, lang) != "" && phs(str(e, lang)) != phs(str(e, "zh")) {
				add(key + ": " + lang + " has other {placeholders} than zh")
			}
			if str(e, lang+"_of") != shortHash(str(e, "zh")) {
				add(key + ": the Chinese changed since the " + lang + " translation")
			}
		}
	}

	page := readText(t, "web/index.html")
	var scripts []literal // file + whole source
	for _, m := range scriptRe.FindAllStringSubmatch(page, -1) {
		scripts = append(scripts, literal{file: "index.html", text: m[1]})
	}
	jsFiles, _ := filepath.Glob("web/js/*.js")
	sort.Strings(jsFiles)
	for _, f := range jsFiles {
		scripts = append(scripts, literal{file: filepath.Base(f), text: readText(t, f)})
	}
	var allScript []string
	var literals []literal
	for _, s := range scripts {
		allScript = append(allScript, s.text)
		literals = append(literals, jsStrings(s.file, s.text)...)
	}
	var goSrc []string
	for _, d := range stringsGoDirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.go"))
		sort.Strings(files)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src := readText(t, f)
			goSrc = append(goSrc, src)
			literals = append(literals, goStrings(filepath.Base(f), src)...)
		}
	}

	used := map[string]bool{}
	attrKeys := map[string]bool{}
	for _, m := range attrKeyRe.FindAllStringSubmatch(page, -1) {
		attrKeys[m[1]] = true
		used[m[1]] = true
	}
	for _, l := range literals {
		if _, ok := words[l.text]; ok {
			used[l.text] = true
		}
	}
	// Catalogue item keys are built as lib.item.<port>.<id>.<name|what>: used
	// when both parts are literals of the catalogue (ptitems tests the reverse).
	cat := map[string]bool{}
	for _, l := range goStrings("ptitems.go", readText(t, "../ptitems/ptitems.go")) {
		cat[l.text] = true
	}
	for _, key := range keys {
		p := strings.Split(key, ".")
		if len(p) == 5 && p[0] == "lib" && p[1] == "item" && cat[p[2]] && cat[p[3]] {
			used[key] = true
		}
	}
	called := map[string]bool{}
	for _, m := range jsCallRe.FindAllStringSubmatch(strings.Join(allScript, "\n"), -1) {
		called[m[1]] = true
	}
	for _, src := range goSrc {
		for _, m := range goCallRe.FindAllStringSubmatch(src, -1) {
			called[m[1]] = true
		}
	}
	for k := range attrKeys {
		called[k] = true
	}
	var unknown []string
	for k := range called {
		if _, ok := words[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		add(k + ": used but not in strings.json")
	}
	for _, k := range keys {
		if !used[k] {
			add(k + ": in strings.json but used nowhere")
		}
	}

	allowed := map[[2]string]bool{}
	for _, x := range doc.NotTranslated {
		f := x.File[strings.LastIndex(x.File, "/")+1:]
		allowed[[2]string{f, x.Text}] = true
	}
	short := func(s string) string {
		r := []rune(s)
		if len(r) > 40 {
			r = r[:40]
		}
		return string(r)
	}
	for _, l := range literals {
		if cjkRe.MatchString(l.text) && !allowed[[2]string{l.file, l.text}] {
			add(l.file + ":" + itoa(l.line) + ": Chinese outside the dictionary: " + short(l.text))
		}
	}
	for _, l := range htmlText(page) {
		if cjkRe.MatchString(l.text) {
			add("index.html:" + itoa(l.line) + ": Chinese in the markup: " + short(l.text))
		}
	}
	for _, p := range problems {
		t.Error(p)
	}
	if len(problems) == 0 {
		t.Logf("%d keys, zh/en/de complete and current", len(words))
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
