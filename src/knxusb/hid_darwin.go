//go:build darwin

package knxusb

// macOS: IOKit's IOHIDManager lists the HID devices, IOHIDDevice moves the
// reports; called through purego so the build stays cgo-free and
// cross-compiles from Windows. See
// $PROD/maps/porttool-on-linux-and-macos/issues/XPT-06-knx-usb-on-macos.md

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	cfStringEncodingUTF8  = 0x08000100
	cfNumberSInt32Type    = 3
	ioHIDReportTypeOutput = 1
	ioReturnNotPermitted  = 0xE00002E2
	ioReturnTimeout       = 0xE00002D6
	pathPrefix            = "DevSrvsID:"

	// writeTimeout is the Windows version's too (hid_windows.go WriteReport).
	writeTimeout = 2 * time.Second
)

var (
	cfRelease                 func(uintptr)
	cfGetTypeID               func(uintptr) uintptr
	cfStringGetTypeID         func() uintptr
	cfNumberGetTypeID         func() uintptr
	cfStringCreateWithCString func(alloc uintptr, s *byte, enc uint32) uintptr
	cfStringGetCString        func(s uintptr, buf *byte, size int, enc uint32) bool
	cfNumberGetValue          func(n uintptr, typ int, out unsafe.Pointer) bool
	cfSetGetCount             func(set uintptr) int
	cfSetGetValues            func(set uintptr, vals *uintptr)
	cfRunLoopGetCurrent       func() uintptr
	cfRunLoopRunInMode        func(mode uintptr, seconds float64, returnAfterSource bool) int32
	cfRunLoopStop             func(rl uintptr)

	ioHIDManagerCreate               func(alloc uintptr, opts uint32) uintptr
	ioHIDManagerSetDeviceMatching    func(m, dict uintptr)
	ioHIDManagerCopyDevices          func(m uintptr) uintptr
	ioHIDDeviceGetProperty           func(d, key uintptr) uintptr
	ioHIDDeviceGetService            func(d uintptr) uint32
	ioRegistryEntryGetRegistryID     func(svc uint32, id *uint64) int32
	ioRegistryEntryIDMatching        func(id uint64) uintptr
	ioServiceGetMatchingService      func(port uint32, dict uintptr) uint32
	ioObjectRelease                  func(obj uint32) int32
	ioHIDDeviceCreate                func(alloc uintptr, svc uint32) uintptr
	ioHIDDeviceOpen                  func(d uintptr, opts uint32) int32
	ioHIDDeviceClose                 func(d uintptr, opts uint32) int32
	ioHIDDeviceSetReportCB           func(d uintptr, typ uint32, id int, r *byte, n int, timeout float64, cb, ctx uintptr) int32
	ioHIDDeviceRegisterInputReport   func(d uintptr, buf *byte, n int, cb, ctx uintptr)
	ioHIDDeviceRegisterRemoval       func(d uintptr, cb, ctx uintptr)
	ioHIDDeviceScheduleWithRunLoop   func(d, rl, mode uintptr)
	ioHIDDeviceUnscheduleFromRunLoop func(d, rl, mode uintptr)

	libOnce sync.Once
	libErr  error

	// CFStrings made once and kept for the life of the process.
	keyVID, keyPID, keyProduct, keyPage, keyInSize, keyOutSize, keyTransport, runMode uintptr

	inputCB, removalCB, setReportCB uintptr
)

func loadLibs() error {
	libOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil { // RegisterLibFunc panics on a missing symbol
				libErr = fmt.Errorf("IOKit: %v", r)
			}
		}()
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			libErr = err
			return
		}
		iokit, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			libErr = err
			return
		}
		for _, f := range []struct {
			fn   any
			lib  uintptr
			name string
		}{
			{&cfRelease, cf, "CFRelease"},
			{&cfGetTypeID, cf, "CFGetTypeID"},
			{&cfStringGetTypeID, cf, "CFStringGetTypeID"},
			{&cfNumberGetTypeID, cf, "CFNumberGetTypeID"},
			{&cfStringCreateWithCString, cf, "CFStringCreateWithCString"},
			{&cfStringGetCString, cf, "CFStringGetCString"},
			{&cfNumberGetValue, cf, "CFNumberGetValue"},
			{&cfSetGetCount, cf, "CFSetGetCount"},
			{&cfSetGetValues, cf, "CFSetGetValues"},
			{&cfRunLoopGetCurrent, cf, "CFRunLoopGetCurrent"},
			{&cfRunLoopRunInMode, cf, "CFRunLoopRunInMode"},
			{&cfRunLoopStop, cf, "CFRunLoopStop"},
			{&ioHIDManagerCreate, iokit, "IOHIDManagerCreate"},
			{&ioHIDManagerSetDeviceMatching, iokit, "IOHIDManagerSetDeviceMatching"},
			{&ioHIDManagerCopyDevices, iokit, "IOHIDManagerCopyDevices"},
			{&ioHIDDeviceGetProperty, iokit, "IOHIDDeviceGetProperty"},
			{&ioHIDDeviceGetService, iokit, "IOHIDDeviceGetService"},
			{&ioRegistryEntryGetRegistryID, iokit, "IORegistryEntryGetRegistryEntryID"},
			{&ioRegistryEntryIDMatching, iokit, "IORegistryEntryIDMatching"},
			{&ioServiceGetMatchingService, iokit, "IOServiceGetMatchingService"},
			{&ioObjectRelease, iokit, "IOObjectRelease"},
			{&ioHIDDeviceCreate, iokit, "IOHIDDeviceCreate"},
			{&ioHIDDeviceOpen, iokit, "IOHIDDeviceOpen"},
			{&ioHIDDeviceClose, iokit, "IOHIDDeviceClose"},
			{&ioHIDDeviceSetReportCB, iokit, "IOHIDDeviceSetReportWithCallback"},
			{&ioHIDDeviceRegisterInputReport, iokit, "IOHIDDeviceRegisterInputReportCallback"},
			{&ioHIDDeviceRegisterRemoval, iokit, "IOHIDDeviceRegisterRemovalCallback"},
			{&ioHIDDeviceScheduleWithRunLoop, iokit, "IOHIDDeviceScheduleWithRunLoop"},
			{&ioHIDDeviceUnscheduleFromRunLoop, iokit, "IOHIDDeviceUnscheduleFromRunLoop"},
		} {
			purego.RegisterLibFunc(f.fn, f.lib, f.name)
		}
		keyVID, keyPID, keyProduct = cfStr("VendorID"), cfStr("ProductID"), cfStr("Product")
		keyPage, keyInSize, keyOutSize = cfStr("PrimaryUsagePage"), cfStr("MaxInputReportSize"), cfStr("MaxOutputReportSize")
		keyTransport = cfStr("Transport")
		// A private run loop mode: only this file's sources run in it.
		runMode = cfStr("PortToolKNXUSB")
		inputCB = purego.NewCallback(onInputReport)
		removalCB = purego.NewCallback(onRemoval)
		setReportCB = purego.NewCallback(onSetReport)
	})
	return libErr
}

func cfStr(s string) uintptr {
	b := append([]byte(s), 0)
	return cfStringCreateWithCString(0, &b[0], cfStringEncodingUTF8)
}

func propInt(dev, key uintptr) (int, bool) {
	v := ioHIDDeviceGetProperty(dev, key)
	if v == 0 || cfGetTypeID(v) != cfNumberGetTypeID() {
		return 0, false
	}
	var n int32
	if !cfNumberGetValue(v, cfNumberSInt32Type, unsafe.Pointer(&n)) {
		return 0, false
	}
	return int(n), true
}

func propString(dev, key uintptr) string {
	v := ioHIDDeviceGetProperty(dev, key)
	if v == 0 || cfGetTypeID(v) != cfStringGetTypeID() {
		return ""
	}
	buf := make([]byte, 256)
	if !cfStringGetCString(v, &buf[0], len(buf), cfStringEncodingUTF8) {
		return ""
	}
	if i := strings.IndexByte(string(buf), 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}

func listHID() ([]DeviceInfo, error) {
	if err := loadLibs(); err != nil {
		return nil, err
	}
	m := ioHIDManagerCreate(0, 0)
	if m == 0 {
		return nil, errors.New("IOHIDManagerCreate failed")
	}
	defer cfRelease(m)
	ioHIDManagerSetDeviceMatching(m, 0) // every HID device; filtered below
	set := ioHIDManagerCopyDevices(m)
	if set == 0 {
		return nil, nil
	}
	defer cfRelease(set)
	devs := make([]uintptr, cfSetGetCount(set))
	if len(devs) == 0 {
		return nil, nil
	}
	cfSetGetValues(set, &devs[0])
	var out []DeviceInfo
	for _, d := range devs {
		page, _ := propInt(d, keyPage)
		in, _ := propInt(d, keyInSize)
		outLen, _ := propInt(d, keyOutSize)
		if !knxShaped(uint16(page), in, outLen) || propString(d, keyTransport) != "USB" {
			continue
		}
		var id uint64
		if ioRegistryEntryGetRegistryID(ioHIDDeviceGetService(d), &id) != 0 {
			continue
		}
		vid, _ := propInt(d, keyVID)
		pid, _ := propInt(d, keyPID)
		// The IORegistry entry ID names one attached HID interface until it is
		// unplugged; the location ID is shared by a composite device's interfaces.
		out = append(out, DeviceInfo{Path: pathPrefix + strconv.FormatUint(id, 10),
			Name: propString(d, keyProduct), VID: uint16(vid), PID: uint16(pid)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

type macHID struct {
	dev    uintptr
	ctx    uintptr
	buf    [ReportLen]byte // IOKit writes input reports here; lives as long as the device
	rl     uintptr
	mu     sync.Mutex
	closed bool
	busy   sync.WaitGroup
	done   chan struct{} // Close was called
	gone   chan struct{} // the device was unplugged
	goneMu sync.Once
	exited chan struct{} // the run loop thread let go of the device
	rep    chan []byte
}

// Callbacks get a small integer, not a Go pointer, as their context.
var (
	openMu   sync.Mutex
	openDevs = map[uintptr]*macHID{}
	writes   = map[uintptr]*pendingWrite{}
	nextCtx  uintptr
)

// pendingWrite is one report IOKit is sending. IOKit holds data until its
// callback, so the entry keeps it alive even after the write gave up waiting.
type pendingWrite struct {
	dev  uintptr // the macHID's ctx
	data []byte
	res  chan uint32
}

func lookup(ctx uintptr) *macHID {
	openMu.Lock()
	defer openMu.Unlock()
	return openDevs[ctx]
}

func onInputReport(ctx, result uintptr, sender unsafe.Pointer, typ, id uintptr, report unsafe.Pointer, n uintptr) {
	d := lookup(ctx)
	if d == nil || int32(uint32(result)) != 0 || report == nil || n == 0 {
		return
	}
	b := append([]byte(nil), unsafe.Slice((*byte)(report), int(n))...)
	// Blocks the run loop rather than dropping a report; IOKit queues meanwhile.
	select {
	case d.rep <- b:
	case <-d.done:
	}
}

func onSetReport(ctx, result uintptr, sender unsafe.Pointer, typ, id uintptr, report unsafe.Pointer, n uintptr) {
	openMu.Lock()
	w := writes[ctx]
	delete(writes, ctx)
	openMu.Unlock()
	if w != nil {
		w.res <- uint32(result)
	}
}

func onRemoval(ctx, result uintptr, sender unsafe.Pointer) {
	if d := lookup(ctx); d != nil {
		d.goneMu.Do(func() { close(d.gone) })
	}
}

func openHID(path string) (HID, error) {
	if err := loadLibs(); err != nil {
		return nil, err
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(path, pathPrefix), 10, 64)
	if err != nil || !strings.HasPrefix(path, pathPrefix) {
		return nil, fmt.Errorf("%s: not a device path from List", path)
	}
	svc := ioServiceGetMatchingService(0, ioRegistryEntryIDMatching(id)) // consumes the dictionary
	if svc == 0 {
		return nil, fmt.Errorf("%s: device not found (unplugged?)", path)
	}
	dev := ioHIDDeviceCreate(0, svc)
	ioObjectRelease(svc)
	if dev == 0 {
		return nil, fmt.Errorf("%s: IOHIDDeviceCreate failed", path)
	}
	if r := ioHIDDeviceOpen(dev, 0); r != 0 {
		cfRelease(dev)
		if uint32(r) == ioReturnNotPermitted {
			return nil, fmt.Errorf("%s: macOS did not allow opening it (System Settings > Privacy & Security > Input Monitoring may need to allow this program)", path)
		}
		return nil, fmt.Errorf("%s: IOHIDDeviceOpen: IOReturn 0x%08X", path, uint32(r))
	}
	d := &macHID{dev: dev, done: make(chan struct{}), gone: make(chan struct{}),
		exited: make(chan struct{}), rep: make(chan []byte, 64)}
	openMu.Lock()
	nextCtx++
	d.ctx = nextCtx
	openDevs[d.ctx] = d
	openMu.Unlock()
	started := make(chan struct{})
	go d.loop(started)
	<-started
	return d, nil
}

// loop owns the device on one OS thread: its run loop delivers the input
// reports, and on Close it lets go of the device.
func (d *macHID) loop(started chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d.rl = cfRunLoopGetCurrent()
	ioHIDDeviceRegisterInputReport(d.dev, &d.buf[0], len(d.buf), inputCB, d.ctx)
	ioHIDDeviceRegisterRemoval(d.dev, removalCB, d.ctx)
	ioHIDDeviceScheduleWithRunLoop(d.dev, d.rl, runMode)
	close(started)
	// Short slices, so a CFRunLoopStop that came before the loop ran is not lost.
	for !d.isClosed() {
		cfRunLoopRunInMode(runMode, 0.2, false)
	}
	ioHIDDeviceRegisterInputReport(d.dev, &d.buf[0], len(d.buf), 0, 0)
	ioHIDDeviceRegisterRemoval(d.dev, 0, 0)
	ioHIDDeviceUnscheduleFromRunLoop(d.dev, d.rl, runMode)
	ioHIDDeviceClose(d.dev, 0)
	cfRelease(d.dev)
	openMu.Lock()
	delete(openDevs, d.ctx)
	for k, w := range writes {
		if w.dev == d.ctx {
			delete(writes, k)
		}
	}
	openMu.Unlock()
	close(d.exited)
}

func (d *macHID) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

func (d *macHID) ReadReport() ([]byte, error) {
	select {
	case r := <-d.rep:
		return r, nil
	case <-d.done:
		return nil, errors.New("closed")
	case <-d.gone:
		return nil, errors.New("the device was unplugged")
	}
}

func (d *macHID) WriteReport(r []byte) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("closed")
	}
	d.busy.Add(1)
	d.mu.Unlock()
	defer d.busy.Done()
	if len(r) == 0 {
		return errors.New("empty report")
	}
	// IOKit wants the report ID beside the buffer, and the buffer without it
	// only when the ID is 0 (no numbered reports). A copy: IOKit may still hold
	// it after this returns on a timeout.
	data := append([]byte(nil), r...)
	if r[0] == 0 {
		data = data[1:]
	}
	if len(data) == 0 {
		return errors.New("empty report")
	}
	w := &pendingWrite{dev: d.ctx, data: data, res: make(chan uint32, 1)}
	openMu.Lock()
	nextCtx++
	wctx := nextCtx
	writes[wctx] = w
	openMu.Unlock()
	// Asynchronous: the callback comes on the device's run loop (loop).
	if ret := ioHIDDeviceSetReportCB(d.dev, ioHIDReportTypeOutput, int(r[0]), &data[0], len(data),
		writeTimeout.Seconds(), setReportCB, wctx); ret != 0 {
		openMu.Lock()
		delete(writes, wctx)
		openMu.Unlock()
		return fmt.Errorf("IOHIDDeviceSetReportWithCallback: IOReturn 0x%08X", uint32(ret))
	}
	select {
	case res := <-w.res:
		switch {
		case res == ioReturnTimeout:
			return errors.New("write timed out")
		case res != 0:
			return fmt.Errorf("IOHIDDeviceSetReportWithCallback: IOReturn 0x%08X", res)
		}
		return nil
	case <-d.gone:
		return errors.New("the device was unplugged")
	// A margin past IOKit's own timeout, in case its callback never comes.
	case <-time.After(writeTimeout + time.Second):
		return errors.New("write timed out")
	}
}

// Close ends a read that is waiting, lets a write finish, then has the run
// loop thread let go of the device.
func (d *macHID) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()
	close(d.done)
	d.busy.Wait()
	cfRunLoopStop(d.rl)
	select {
	case <-d.exited:
	case <-time.After(2 * time.Second):
		return errors.New("the device did not let go in time")
	}
	return nil
}
