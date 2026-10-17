package main

// A thin playwright-like layer over chromedp: locators resolved in the page,
// actions wait until the element is there and actionable, and a locator that
// matches more than one element refuses an action (playwright's strict mode).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const defaultTimeout = 10 * time.Second

// pwError is what a failed browser action panics with; try turns it back into an error.
type pwError struct{ msg string }

func (e pwError) Error() string { return e.msg }

func throw(format string, a ...any) { panic(pwError{fmt.Sprintf(format, a...)}) }

// try runs f and returns what it panicked with, like a Python try / except
// Exception around playwright calls.
func try(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	f()
	return nil
}

// Page is one browser tab.
type Page struct {
	ctx     context.Context
	url     string
	mx, my  float64
	dlDir   string
	mu      sync.Mutex
	onReq   func(*network.EventRequestWillBeSent)
	dlBegin map[string]string // guid -> suggested name
	dlDone  chan string       // guid
}

// ---------------------------------------------------------------- evaluation

// The page-side half: locator resolution and the per-element operations.
const helperJS = `(() => {
  const order = a => [...new Set(a)].sort((x, y) => x === y ? 0 :
      (x.compareDocumentPosition(y) & Node.DOCUMENT_POSITION_FOLLOWING) ? -1 : 1);
  const q = steps => {
    let els = [document];
    for (const s of steps) {
      if (s.css !== undefined) {
        const out = [];
        for (const e of els) { for (const x of e.querySelectorAll(s.css)) { out.push(x); } }
        els = order(out);
      } else if (s.xpath !== undefined) {
        const out = [];
        for (const e of els) {
          const r = document.evaluate(s.xpath, e, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
          for (let i = 0; i < r.snapshotLength; i++) { const n = r.snapshotItem(i); if (n.nodeType === 1) { out.push(n); } }
        }
        els = order(out);
      } else if (s.nth !== undefined) {
        const i = s.nth < 0 ? els.length + s.nth : s.nth;
        els = i >= 0 && i < els.length ? [els[i]] : [];
      } else if (s.text !== undefined) {
        const t = s.text.toLowerCase();
        els = els.filter(e => (e.textContent || '').replace(/\s+/g, ' ').toLowerCase().includes(t));
      }
    }
    return els.filter(e => e !== document);
  };
  // Playwright's isElementVisible, display: contents included.
  const textVisible = n => { const r = document.createRange(); r.selectNode(n); const b = r.getBoundingClientRect(); return b.width > 0 && b.height > 0; };
  const visible = e => {
    const st = getComputedStyle(e);
    if (st.display === 'contents') {
      for (let c = e.firstChild; c; c = c.nextSibling) {
        if (c.nodeType === 1 && visible(c)) { return true; }
        if (c.nodeType === 3 && textVisible(c)) { return true; }
      }
      return false;
    }
    if (e.checkVisibility && !e.checkVisibility()) { return false; }
    if (st.visibility !== 'visible') { return false; }
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const disabled = e => {
    if (['BUTTON', 'INPUT', 'SELECT', 'TEXTAREA', 'OPTION', 'OPTGROUP'].includes(e.tagName)) {
      if (e.hasAttribute('disabled')) { return true; }
      const fs = e.closest('fieldset[disabled]');
      if (fs) { const lg = fs.querySelector(':scope > legend'); if (!lg || !lg.contains(e)) { return true; } }
    }
    return e.getAttribute('aria-disabled') === 'true';
  };
  const checked = e => {
    if (e.tagName === 'INPUT' && ['checkbox', 'radio'].includes(e.type)) { return e.checked; }
    const a = e.getAttribute('aria-checked');
    if (a === 'true') { return true; }
    if (a === 'false') { return false; }
    throw new Error('Not a checkbox or radio button');
  };
  const frame = () => new Promise(r => requestAnimationFrame(() => r()));
  const ops = {
    text: e => e.innerText,
    textContent: e => e.textContent,
    attr: (e, n) => e.getAttribute(n),
    visible: e => visible(e),
    checked: e => checked(e),
    disabled: e => disabled(e),
    value: e => {
      if (!['INPUT', 'TEXTAREA', 'SELECT'].includes(e.tagName)) { throw new Error('Not an <input>, <textarea> or <select> element'); }
      return e.value;
    },
    box: e => {
      if (!e.getClientRects().length) { return null; }
      const r = e.getBoundingClientRect();
      return {x: r.x, y: r.y, width: r.width, height: r.height};
    },
    dispatch: (e, t) => { e.dispatchEvent(new Event(t, {bubbles: true, cancelable: true, composed: true})); return true; },
    clickpoint: async e => {
      if (!e.isConnected) { return {wait: 'element is not attached'}; }
      if (!visible(e)) { return {wait: 'element is not visible'}; }
      if (disabled(e)) { return {wait: 'element is not enabled'}; }
      if (e.scrollIntoViewIfNeeded) { e.scrollIntoViewIfNeeded(true); } else { e.scrollIntoView({block: 'center'}); }
      const a = e.getBoundingClientRect();
      await frame(); await frame();
      const b = e.getBoundingClientRect();
      if (a.x !== b.x || a.y !== b.y || a.width !== b.width || a.height !== b.height) { return {wait: 'element is not stable'}; }
      const x = b.x + b.width / 2, y = b.y + b.height / 2;
      const hit = document.elementFromPoint(x, y);
      if (!hit || !(hit === e || e.contains(hit))) {
        return {wait: 'element does not receive pointer events: ' + (hit ? hit.outerHTML.slice(0, 120) : 'nothing')};
      }
      return {x, y};
    },
    fillprep: (e, v) => {
      if (!visible(e)) { return {wait: 'element is not visible'}; }
      if (disabled(e)) { return {wait: 'element is not enabled'}; }
      if (e.readOnly) { return {wait: 'element is not editable'}; }
      if (e.tagName === 'INPUT') {
        const t = (e.type || 'text').toLowerCase();
        if (['color', 'date', 'time', 'datetime-local', 'month', 'range', 'week'].includes(t)) {
          e.focus(); e.value = v;
          e.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
          e.dispatchEvent(new Event('change', {bubbles: true}));
          return {done: true};
        }
        if (t === 'number' && v.trim() !== '' && isNaN(Number(v.trim()))) { throw new Error('Cannot type text into input[type=number]'); }
        e.focus(); e.select();
      } else if (e.tagName === 'TEXTAREA') {
        e.focus(); e.select();
      } else if (e.isContentEditable) {
        e.focus(); const r = document.createRange(); r.selectNodeContents(e);
        const s = getSelection(); s.removeAllRanges(); s.addRange(r);
      } else {
        throw new Error('Element is not an <input>, <textarea> or [contenteditable] element');
      }
      if (v === '') { document.execCommand('delete'); return {done: true}; }
      return {done: false};
    },
    select: (e, v) => {
      if (e.tagName !== 'SELECT') { throw new Error('Element is not a <select> element'); }
      if (!visible(e)) { return {wait: 'element is not visible'}; }
      if (disabled(e)) { return {wait: 'element is not enabled'}; }
      const m = [...e.options].find(o => o.value === v || o.label === v);
      if (!m) { return {wait: 'no option ' + JSON.stringify(v)}; }
      for (const o of e.options) { o.selected = false; }
      m.selected = true;
      e.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
      e.dispatchEvent(new Event('change', {bubbles: true}));
      return {done: true};
    },
  };
  return {
    q,
    count: steps => q(steps).length,
    one: async (steps, op, arg) => {
      const els = q(steps);
      if (els.length !== 1) { return {n: els.length}; }
      return {n: 1, v: await ops[op](els[0], arg)};
    },
    first: async (steps, op, arg) => {
      const els = q(steps);
      if (!els.length) { return {n: 0}; }
      return {n: els.length, v: await ops[op](els[0], arg)};
    },
    all: async (steps, op, arg) => { const out = []; for (const e of q(steps)) { out.push(await ops[op](e, arg)); } return out; },
  };
})()`

// Eval runs js the way playwright's page.evaluate does: an expression, or a
// function called with arg; promises are awaited. A JS exception panics.
func (p *Page) Eval(js string, arg ...any) J {
	v, _, err := p.eval(js, argOf(arg))
	if err != nil {
		throw("page.evaluate: %v", err)
	}
	return v
}

func argOf(arg []any) any {
	if len(arg) == 0 {
		return nil
	}
	return arg[0]
}

func (p *Page) eval(js string, arg any) (J, bool, error) {
	a, err := json.Marshal(arg)
	if err != nil {
		return J{}, false, err
	}
	src := "(async () => { window.__t4 = window.__t4 || " + helperJS + ";\nconst __f = (\n" + js +
		"\n); const __v = typeof __f === 'function' ? await __f(" + string(a) + ") : await __f;" +
		" return JSON.stringify({v: __v === undefined ? null : __v, t: !!__v}); })()"
	ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute)
	defer cancel()
	var out string
	err = chromedp.Run(ctx, chromedp.Evaluate(src, &out, func(e *runtime.EvaluateParams) *runtime.EvaluateParams {
		return e.WithAwaitPromise(true)
	}))
	if err != nil {
		return J{}, false, err
	}
	var r struct {
		V any  `json:"v"`
		T bool `json:"t"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return J{}, false, err
	}
	return J{r.V}, r.T, nil
}

// WaitFunc polls js (expression or function of arg) until it is truthy and
// returns that value; ms 0 is the default timeout.
func (p *Page) WaitFunc(js string, arg any, ms int) J {
	d := timeoutOf(ms)
	end := time.Now().Add(d)
	for {
		v, ok, err := p.eval(js, arg)
		if err != nil {
			throw("page.wait_for_function: %v", err)
		}
		if ok {
			return v
		}
		if time.Now().After(end) {
			throw("Timeout %dms exceeded waiting for function", d.Milliseconds())
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func timeoutOf(ms int) time.Duration {
	if ms <= 0 {
		return defaultTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// Wait is page.wait_for_timeout.
func (p *Page) Wait(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// WaitSel waits for the first match of a CSS selector to be visible.
func (p *Page) WaitSel(sel string, ms int) { p.WaitSelState(sel, "visible", ms) }

// WaitSelState: state is "visible", "attached" or "hidden".
func (p *Page) WaitSelState(sel, state string, ms int) {
	d := timeoutOf(ms)
	end := time.Now().Add(d)
	steps := []step{{CSS: &sel}}
	for {
		r := p.call("first", steps, "visible", nil)
		n := int(r.K("n").N())
		vis := r.K("v").B()
		switch {
		case state == "attached" && n > 0, state == "visible" && n > 0 && vis, state == "hidden" && (n == 0 || !vis):
			return
		}
		if time.Now().After(end) {
			throw("Timeout %dms exceeded waiting for selector %q to be %s", d.Milliseconds(), sel, state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *Page) call(fn string, steps []step, op string, arg any) J {
	b, _ := json.Marshal(steps)
	a, _ := json.Marshal(arg)
	return p.Eval("__t4." + fn + "(" + string(b) + ", " + jsStr(op) + ", " + string(a) + ")")
}

func jsStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// Goto, Reload: wait for the load event, as playwright does.
func (p *Page) Goto(url string) {
	if err := chromedp.Run(p.ctx, chromedp.Navigate(url)); err != nil {
		throw("page.goto: %v", err)
	}
	p.url = url
}

func (p *Page) Reload() {
	if err := chromedp.Run(p.ctx, chromedp.Reload()); err != nil {
		throw("page.reload: %v", err)
	}
}

// Press sends one key with no text, as playwright's keyboard.press does for Escape.
func (p *Page) Press(key string, code int64) {
	err := chromedp.Run(p.ctx,
		input.DispatchKeyEvent(input.KeyRawDown).WithKey(key).WithCode(key).WithWindowsVirtualKeyCode(code).WithNativeVirtualKeyCode(code),
		input.DispatchKeyEvent(input.KeyUp).WithKey(key).WithCode(key).WithWindowsVirtualKeyCode(code).WithNativeVirtualKeyCode(code))
	if err != nil {
		throw("keyboard.press: %v", err)
	}
}

// MouseMove moves from the last position in steps, like playwright's mouse.move.
func (p *Page) MouseMove(x, y float64, steps int, buttonDown bool) {
	if steps < 1 {
		steps = 1
	}
	fx, fy := p.mx, p.my
	for i := 1; i <= steps; i++ {
		ev := input.DispatchMouseEvent(input.MouseMoved, fx+(x-fx)*float64(i)/float64(steps), fy+(y-fy)*float64(i)/float64(steps))
		if buttonDown {
			ev = ev.WithButton(input.Left).WithButtons(1)
		}
		if err := chromedp.Run(p.ctx, ev); err != nil {
			throw("mouse.move: %v", err)
		}
	}
	p.mx, p.my = x, y
}

func (p *Page) MouseDown() {
	if err := chromedp.Run(p.ctx, input.DispatchMouseEvent(input.MousePressed, p.mx, p.my).WithButton(input.Left).WithButtons(1).WithClickCount(1)); err != nil {
		throw("mouse.down: %v", err)
	}
}

func (p *Page) MouseUp() {
	if err := chromedp.Run(p.ctx, input.DispatchMouseEvent(input.MouseReleased, p.mx, p.my).WithButton(input.Left).WithClickCount(1)); err != nil {
		throw("mouse.up: %v", err)
	}
}

// ExpectRequest runs act and returns the body of the first request whose URL ends with suffix.
func (p *Page) ExpectRequest(suffix string, act func()) string {
	got := make(chan *network.EventRequestWillBeSent, 1)
	p.mu.Lock()
	p.onReq = func(ev *network.EventRequestWillBeSent) {
		if strings.HasSuffix(ev.Request.URL, suffix) {
			select {
			case got <- ev:
			default:
			}
		}
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.onReq = nil; p.mu.Unlock() }()
	act()
	select {
	case ev := <-got:
		var b strings.Builder
		for _, e := range ev.Request.PostDataEntries {
			d, _ := base64.StdEncoding.DecodeString(e.Bytes)
			b.Write(d)
		}
		if b.Len() == 0 && ev.Request.HasPostData {
			var body string
			_ = chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				var err error
				body, err = network.GetRequestPostData(ev.RequestID).Do(ctx)
				return err
			}))
			return body
		}
		return b.String()
	case <-time.After(defaultTimeout):
		throw("Timeout %dms exceeded while waiting for a request to %s", defaultTimeout.Milliseconds(), suffix)
	}
	return ""
}

// ExpectDownload runs act and returns the suggested file name and the saved file.
func (p *Page) ExpectDownload(ms int, act func()) (name, path string) {
	p.mu.Lock()
	p.dlBegin = map[string]string{}
	p.dlDone = make(chan string, 4)
	p.mu.Unlock()
	act()
	select {
	case guid := <-p.dlDone:
		p.mu.Lock()
		name = p.dlBegin[guid]
		p.mu.Unlock()
		return name, filepath.Join(p.dlDir, guid)
	case <-time.After(timeoutOf(ms)):
		throw("Timeout %dms exceeded while waiting for event \"download\"", timeoutOf(ms).Milliseconds())
	}
	return "", ""
}

// listen routes the tab's events: requests, downloads, and page errors to onError.
func (p *Page) listen(onError func(msg, tag string)) {
	handle := func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			p.mu.Lock()
			f := p.onReq
			p.mu.Unlock()
			if f != nil {
				f(e)
			}
		case *browser.EventDownloadWillBegin:
			p.mu.Lock()
			if p.dlBegin != nil {
				p.dlBegin[e.GUID] = e.SuggestedFilename
			}
			p.mu.Unlock()
		case *browser.EventDownloadProgress:
			if e.State == browser.DownloadProgressStateCompleted {
				p.mu.Lock()
				ch := p.dlDone
				p.mu.Unlock()
				if ch != nil {
					select {
					case ch <- e.GUID:
					default:
					}
				}
			}
		default:
			if onError != nil {
				if msg, tag := pageErrorOf(ev); msg != "" {
					onError(msg, tag)
				}
			}
		}
	}
	chromedp.ListenTarget(p.ctx, handle)
	chromedp.ListenBrowser(p.ctx, handle)
}

func (p *Page) allowDownloads() error {
	dir, err := os.MkdirTemp("", "porttool_t402_dl")
	if err != nil {
		return err
	}
	p.dlDir = dir
	return chromedp.Run(p.ctx, browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).
		WithDownloadPath(dir).WithEventsEnabled(true))
}

// compileOnly reports a syntax error in js without running it, like node --check.
func (p *Page) compileOnly(js, name string) (string, error) {
	var msg string
	err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, exc, err := runtime.CompileScript(js, name, false).Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			msg = fmt.Sprintf("%s:%d:%d %s", name, exc.LineNumber+1, exc.ColumnNumber+1, exc.Text)
			if exc.Exception != nil && exc.Exception.Description != "" {
				msg += " " + strings.SplitN(exc.Exception.Description, "\n", 2)[0]
			}
		}
		return nil
	}))
	return msg, err
}

// ---------------------------------------------------------------- locators

type step struct {
	CSS   *string `json:"css,omitempty"`
	XPath *string `json:"xpath,omitempty"`
	Nth   *int    `json:"nth,omitempty"`
	Text  *string `json:"text,omitempty"`
}

// Loc is a lazy chain of selectors, resolved again on every use.
type Loc struct {
	p     *Page
	steps []step
}

func selStep(sel string) step {
	if x, ok := strings.CutPrefix(sel, "xpath="); ok {
		return step{XPath: &x}
	}
	return step{CSS: &sel}
}

func (p *Page) L(sel string) *Loc { return &Loc{p, []step{selStep(sel)}} }

func (l *Loc) with(s step) *Loc {
	st := append(append([]step{}, l.steps...), s)
	return &Loc{l.p, st}
}

func (l *Loc) L(sel string) *Loc { return l.with(selStep(sel)) }
func (l *Loc) Nth(i int) *Loc    { return l.with(step{Nth: &i}) }
func (l *Loc) First() *Loc       { return l.Nth(0) }
func (l *Loc) Last() *Loc        { return l.Nth(-1) }

// HasText is filter(has_text=...): case-insensitive substring of the text content.
func (l *Loc) HasText(t string) *Loc { return l.with(step{Text: &t}) }

func (l *Loc) String() string { b, _ := json.Marshal(l.steps); return string(b) }

func (l *Loc) Count() int { return int(l.p.Eval("__t4.count(" + mustJSON(l.steps) + ")").N()) }

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// one waits until exactly one element matches and runs op on it; more than one is a strict-mode violation.
func (l *Loc) one(op string, arg any, d time.Duration) J {
	end := time.Now().Add(d)
	for {
		r := l.p.call("one", l.steps, op, arg)
		n := int(r.K("n").N())
		if n > 1 {
			throw("strict mode violation: %s resolved to %d elements", l, n)
		}
		if n == 1 {
			return r.K("v")
		}
		if time.Now().After(end) {
			throw("Timeout %dms exceeded waiting for %s", d.Milliseconds(), l)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// action retries op until it reports neither wait nor a missing element.
func (l *Loc) action(op string, arg any) J {
	end := time.Now().Add(defaultTimeout)
	why := ""
	for {
		r := l.p.call("one", l.steps, op, arg)
		n := int(r.K("n").N())
		if n > 1 {
			throw("strict mode violation: %s resolved to %d elements", l, n)
		}
		if n == 1 {
			v := r.K("v")
			if w := v.K("wait").S(); w == "" {
				return v
			} else {
				why = w
			}
		} else {
			why = "no element"
		}
		if time.Now().After(end) {
			throw("Timeout %dms exceeded: %s %s - %s", defaultTimeout.Milliseconds(), op, l, why)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (l *Loc) Text() string        { return l.one("text", nil, defaultTimeout).S() }
func (l *Loc) TextContent() string { return l.one("textContent", nil, defaultTimeout).S() }

// Attr is get_attribute with a missing attribute as "".
func (l *Loc) Attr(name string) string { return l.one("attr", name, defaultTimeout).S() }

// AttrNil is get_attribute(name) is None.
func (l *Loc) AttrNil(name string) bool { return l.one("attr", name, defaultTimeout).Nil() }

func (l *Loc) Checked() bool  { return l.one("checked", nil, defaultTimeout).B() }
func (l *Loc) Disabled() bool { return l.one("disabled", nil, defaultTimeout).B() }
func (l *Loc) Enabled() bool  { return !l.Disabled() }
func (l *Loc) Value() string  { return l.one("value", nil, defaultTimeout).S() }

// Visible does not wait: no element is not visible.
func (l *Loc) Visible() bool {
	r := l.p.call("one", l.steps, "visible", nil)
	n := int(r.K("n").N())
	if n > 1 {
		throw("strict mode violation: %s resolved to %d elements", l, n)
	}
	return n == 1 && r.K("v").B()
}

func (l *Loc) Hidden() bool { return !l.Visible() }

type Box struct{ X, Y, W, H float64 }

// Box is bounding_box: nil when the element is not rendered.
func (l *Loc) Box() *Box {
	v := l.one("box", nil, defaultTimeout)
	if v.Nil() {
		return nil
	}
	return &Box{v.K("x").N(), v.K("y").N(), v.K("width").N(), v.K("height").N()}
}

func (l *Loc) Click() {
	v := l.action("clickpoint", nil)
	x, y := v.K("x").N(), v.K("y").N()
	l.p.MouseMove(x, y, 1, false)
	l.p.MouseDown()
	l.p.MouseUp()
}

func (l *Loc) SetChecked(on bool) {
	if l.Checked() == on {
		return
	}
	l.Click()
	if l.Checked() != on {
		throw("Clicking the checkbox did not change its state: %s", l)
	}
}

func (l *Loc) Check()   { l.SetChecked(true) }
func (l *Loc) Uncheck() { l.SetChecked(false) }

func (l *Loc) Fill(v string) {
	if l.action("fillprep", v).K("done").B() {
		return
	}
	if err := chromedp.Run(l.p.ctx, input.InsertText(v)); err != nil {
		throw("fill: %v", err)
	}
}

// Select is select_option by value or label.
func (l *Loc) Select(v string) { l.action("select", v) }

// Dispatch is dispatch_event(type).
func (l *Loc) Dispatch(typ string) { l.one("dispatch", typ, defaultTimeout) }

// WaitFor waits for exactly one element to be visible.
func (l *Loc) WaitFor(ms int) {
	end := time.Now().Add(timeoutOf(ms))
	for {
		r := l.p.call("one", l.steps, "visible", nil)
		n := int(r.K("n").N())
		if n > 1 {
			throw("strict mode violation: %s resolved to %d elements", l, n)
		}
		if n == 1 && r.K("v").B() {
			return
		}
		if time.Now().After(end) {
			throw("Timeout %dms exceeded waiting for %s to be visible", timeoutOf(ms).Milliseconds(), l)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// AllTexts is all_inner_texts; AllTextContents is all_text_contents.
func (l *Loc) AllTexts() []string { return l.p.call("all", l.steps, "text", nil).Strings() }
func (l *Loc) AllTextContents() []string {
	return l.p.call("all", l.steps, "textContent", nil).Strings()
}

// All is locator.all(): one locator per current match.
func (l *Loc) All() []*Loc {
	n := l.Count()
	out := make([]*Loc, n)
	for i := range out {
		out[i] = l.Nth(i)
	}
	return out
}

// EvalAll is evaluate_all: fn gets the array of matches.
func (l *Loc) EvalAll(fn string) J {
	return l.p.Eval("(() => { const __els = __t4.q(" + mustJSON(l.steps) + "); return (" + fn + ")(__els); })()")
}

// ---------------------------------------------------------------- JSON values

// J is a decoded JSON value read leniently, like Python's .get chains.
type J struct{ v any }

func (j J) K(k string) J {
	if m, ok := j.v.(map[string]any); ok {
		return J{m[k]}
	}
	return J{}
}

func (j J) Has(k string) bool {
	m, ok := j.v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m[k]
	return ok
}

func (j J) I(i int) J {
	if a, ok := j.v.([]any); ok && i >= 0 && i < len(a) {
		return J{a[i]}
	}
	return J{}
}

func (j J) S() string  { s, _ := j.v.(string); return s }
func (j J) N() float64 { f, _ := j.v.(float64); return f }
func (j J) B() bool    { b, _ := j.v.(bool); return b }
func (j J) Nil() bool  { return j.v == nil }

func (j J) IsNum() bool { _, ok := j.v.(float64); return ok }

func (j J) Len() int {
	switch v := j.v.(type) {
	case []any:
		return len(v)
	case map[string]any:
		return len(v)
	case string:
		return len(v)
	}
	return 0
}

func (j J) Arr() []J {
	a, _ := j.v.([]any)
	out := make([]J, len(a))
	for i, x := range a {
		out[i] = J{x}
	}
	return out
}

func (j J) Strings() []string {
	var out []string
	for _, x := range j.Arr() {
		out = append(out, x.S())
	}
	return out
}

func (j J) Keys() []string {
	m, _ := j.v.(map[string]any)
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Truthy is Python truthiness.
func (j J) Truthy() bool {
	switch v := j.v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func (j J) String() string { return show(j.v) }

// show renders a value for a failure detail.
func show(v any) string {
	if j, ok := v.(J); ok {
		v = j.v
	}
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSpace(b.String())
}
