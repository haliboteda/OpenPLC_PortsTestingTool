package ptpanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"PortTool/internal/simboard"
)

// Panel text lives in one dictionary, web/strings.json, embedded with the page
// (decision 82). Go never sends the page a sentence: it sends a msg, a key into
// that dictionary plus its arguments, and the page words it in the language the
// person chose. What Go writes to files is the dictionary's English.
// $PROD/maps/porttool-panel-languages/map.md

type msg struct {
	Key  string
	Args map[string]any
}

// m builds a msg from a key and name/value pairs. An error value becomes the
// msg it carries, or {detail} with its text: library errors stay as they are.
func m(key string, kv ...any) msg {
	x := msg{Key: key}
	if len(kv) > 0 {
		x.Args = map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			v := kv[i+1]
			if err, ok := v.(error); ok {
				v = msgOf(err)
			}
			x.Args[kv[i].(string)] = v
		}
	}
	return x
}

func ref(x msg) *msg { return &x }

// msgError is an error that already knows its wording.
type msgError struct{ msg }

func (e msgError) Error() string { return e.English() }

// msgOf is how an error is shown: its own msg if it has one, otherwise its text.
func msgOf(err error) msg {
	var me msgError
	if errors.As(err, &me) {
		return me.msg
	}
	if errors.Is(err, simboard.ErrNotBuilt) {
		return m("go.sim.not_built")
	}
	var se *simboard.StartError
	if errors.As(err, &se) {
		return m("go.sim.start_failed", "path", se.Path, "detail", se.Err.Error())
	}
	return m("go.detail", "detail", err.Error())
}

// MarshalJSON is what the page reads: {"$t": key, "args": {...}}, or "" for no message.
func (x msg) MarshalJSON() ([]byte, error) {
	if x.Key == "" {
		return []byte(`""`), nil
	}
	return json.Marshal(map[string]any{"$t": x.Key, "args": x.Args})
}

// alert says the page should also put this line on top, not only in the log.
func (x msg) alert() bool { return strings.HasPrefix(x.Key, "go.alert.") }

var placeholder = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// English is the text for files and for clients that are not the page.
func (x msg) English() string {
	if x.Key == "" {
		return ""
	}
	s := x.Key
	if e, ok := dictionary()[x.Key]; ok {
		s = e["en"]
		if s == "" {
			s = e["zh"]
		}
	}
	return placeholder.ReplaceAllStringFunc(s, func(p string) string {
		v, ok := x.Args[p[1:len(p)-1]]
		if !ok {
			return p
		}
		if inner, ok := v.(msg); ok {
			return inner.English()
		}
		return fmt.Sprint(v)
	})
}

var (
	dictOnce sync.Once
	dict     map[string]map[string]string
	deOK     bool
)

// stringsFile is web/strings.json; only the parts Go reads are declared.
type stringsFile struct {
	DeReviewed bool                         `json:"de_reviewed"`
	Strings    map[string]map[string]string `json:"strings"`
}

func loadStrings() stringsFile {
	var f stringsFile
	b, err := webFS.ReadFile("web/strings.json")
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		// Embedded at build time and checked by the self-check, so this is a
		// broken build, not a bench condition.
		panic("web/strings.json: " + err.Error())
	}
	return f
}

func dictionary() map[string]map[string]string {
	dictOnce.Do(func() {
		f := loadStrings()
		dict, deOK = f.Strings, f.DeReviewed
	})
	return dict
}
