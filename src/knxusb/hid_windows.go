package knxusb

// Windows: setupapi.dll lists the HID interfaces, hid.dll describes them,
// overlapped ReadFile / WriteFile move the reports. No cgo.

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type spDeviceInterfaceData struct {
	cbSize   uint32
	guid     guid
	flags    uint32
	reserved uintptr
}

type hiddAttributes struct {
	size    uint32
	vid     uint16
	pid     uint16
	version uint16
}

type hidpCaps struct {
	usage, usagePage                              uint16
	inputReportByteLength, outputReportByteLength uint16
	featureReportByteLength                       uint16
	reserved                                      [17]uint16
	numbers                                       [10]uint16
}

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10
	hidpStatusSuccess    = 0x00110000
	errorNoMoreItems     = 259
	fileFlagOverlapped   = 0x40000000
)

var (
	dllOnce                                                                           sync.Once
	dllErr                                                                            error
	pHidGuid, pHidAttr, pHidProduct, pHidPreparsed, pHidFreePreparsed, pHidCaps       *syscall.LazyProc
	pClassDevs, pEnumIfaces, pIfaceDetail, pDestroyList, pCreateEvent, pOverlappedRes *syscall.LazyProc
)

// systemDLL loads a DLL from the system directory only, never from beside the exe.
func systemDLL(name string) *syscall.LazyDLL {
	buf := make([]uint16, 300)
	// kernel32 is a KnownDLL: Windows always takes it from the system directory.
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW").Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	n := min(int(r), len(buf))
	return syscall.NewLazyDLL(syscall.UTF16ToString(buf[:n]) + `\` + name)
}

func loadDLLs() error {
	dllOnce.Do(func() {
		hid, setup, k32 := systemDLL("hid.dll"), systemDLL("setupapi.dll"), systemDLL("kernel32.dll")
		pHidGuid = hid.NewProc("HidD_GetHidGuid")
		pHidAttr = hid.NewProc("HidD_GetAttributes")
		pHidProduct = hid.NewProc("HidD_GetProductString")
		pHidPreparsed = hid.NewProc("HidD_GetPreparsedData")
		pHidFreePreparsed = hid.NewProc("HidD_FreePreparsedData")
		pHidCaps = hid.NewProc("HidP_GetCaps")
		pClassDevs = setup.NewProc("SetupDiGetClassDevsW")
		pEnumIfaces = setup.NewProc("SetupDiEnumDeviceInterfaces")
		pIfaceDetail = setup.NewProc("SetupDiGetDeviceInterfaceDetailW")
		pDestroyList = setup.NewProc("SetupDiDestroyDeviceInfoList")
		pCreateEvent = k32.NewProc("CreateEventW")
		pOverlappedRes = k32.NewProc("GetOverlappedResult")
		for _, p := range []*syscall.LazyProc{pHidGuid, pHidAttr, pHidProduct, pHidPreparsed, pHidFreePreparsed,
			pHidCaps, pClassDevs, pEnumIfaces, pIfaceDetail, pDestroyList, pCreateEvent, pOverlappedRes} {
			if err := p.Find(); err != nil {
				dllErr = err
				return
			}
		}
	})
	return dllErr
}

func listHID() ([]DeviceInfo, error) {
	if err := loadDLLs(); err != nil {
		return nil, err
	}
	var g guid
	pHidGuid.Call(uintptr(unsafe.Pointer(&g)))
	set, _, err := pClassDevs.Call(uintptr(unsafe.Pointer(&g)), 0, 0, digcfPresent|digcfDeviceInterface)
	if set == uintptr(syscall.InvalidHandle) {
		return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
	}
	defer pDestroyList.Call(set)
	var out []DeviceInfo
	for i := uint32(0); ; i++ {
		d := spDeviceInterfaceData{cbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}
		ok, _, err := pEnumIfaces.Call(set, 0, uintptr(unsafe.Pointer(&g)), uintptr(i), uintptr(unsafe.Pointer(&d)))
		if ok == 0 {
			if errno, _ := err.(syscall.Errno); errno == errorNoMoreItems {
				break
			}
			return out, fmt.Errorf("SetupDiEnumDeviceInterfaces: %w", err)
		}
		path, ok2 := interfacePath(set, &d)
		if !ok2 {
			continue
		}
		if info, ok := describe(path); ok {
			out = append(out, info)
		}
	}
	return out, nil
}

func interfacePath(set uintptr, d *spDeviceInterfaceData) (string, bool) {
	var need uint32
	pIfaceDetail.Call(set, uintptr(unsafe.Pointer(d)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
	if need < 8 {
		return "", false
	}
	buf := make([]uint16, (need+1)/2+1)
	// cbSize is the fixed part: a DWORD and one WCHAR, padded to 8 on 64-bit.
	cb := uint32(6)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		cb = 8
	}
	*(*uint32)(unsafe.Pointer(&buf[0])) = cb
	ok, _, _ := pIfaceDetail.Call(set, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0, 0)
	if ok == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf[2:]), true
}

// describe opens a device for query only (works for keyboards too) and keeps it
// when its capabilities look like a KNX USB interface.
func describe(path string) (DeviceInfo, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return DeviceInfo{}, false
	}
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return DeviceInfo{}, false
	}
	defer syscall.CloseHandle(h)
	var pre uintptr
	if ok, _, _ := pHidPreparsed.Call(uintptr(h), uintptr(unsafe.Pointer(&pre))); ok == 0 {
		return DeviceInfo{}, false
	}
	var caps hidpCaps
	st, _, _ := pHidCaps.Call(pre, uintptr(unsafe.Pointer(&caps)))
	pHidFreePreparsed.Call(pre)
	if st != hidpStatusSuccess || !knxShaped(caps.usagePage, int(caps.inputReportByteLength), int(caps.outputReportByteLength)) {
		return DeviceInfo{}, false
	}
	info := DeviceInfo{Path: path}
	a := hiddAttributes{size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if ok, _, _ := pHidAttr.Call(uintptr(h), uintptr(unsafe.Pointer(&a))); ok != 0 {
		info.VID, info.PID = a.vid, a.pid
	}
	name := make([]uint16, 128)
	if ok, _, _ := pHidProduct.Call(uintptr(h), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2)); ok != 0 {
		info.Name = syscall.UTF16ToString(name)
	}
	return info, true
}

type winHID struct {
	h      syscall.Handle
	mu     sync.Mutex
	closed bool
	rd, wr ioSlot
	busy   sync.WaitGroup
}

// ioSlot is one overlapped operation's state; it lives as long as the device,
// so the kernel never writes into freed memory.
type ioSlot struct {
	ov  syscall.Overlapped
	buf [ReportLen]byte
}

func newEvent() (syscall.Handle, error) {
	h, _, err := pCreateEvent.Call(0, 1, 0, 0)
	if h == 0 {
		return 0, err
	}
	return syscall.Handle(h), nil
}

func openHID(path string) (HID, error) {
	if err := loadDLLs(); err != nil {
		return nil, err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, fileFlagOverlapped, 0)
	if err != nil {
		return nil, err
	}
	d := &winHID{h: h}
	if d.rd.ov.HEvent, err = newEvent(); err == nil {
		d.wr.ov.HEvent, err = newEvent()
	}
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// wait finishes one overlapped call, cancelling it after limit (0: no limit).
func (d *winHID) wait(s *ioSlot, err error, limit time.Duration) (uint32, error) {
	var n uint32
	if err != nil && err != syscall.ERROR_IO_PENDING {
		return 0, err
	}
	// Polled, so a Close that came between begin and the call still ends it.
	start := time.Now()
	for {
		if ev, _ := syscall.WaitForSingleObject(s.ov.HEvent, 200); ev == syscall.WAIT_OBJECT_0 {
			break
		}
		d.mu.Lock()
		stop := d.closed
		d.mu.Unlock()
		if stop || (limit > 0 && time.Since(start) > limit) {
			_ = syscall.CancelIoEx(d.h, &s.ov)
			break
		}
	}
	ok, _, e := pOverlappedRes.Call(uintptr(d.h), uintptr(unsafe.Pointer(&s.ov)), uintptr(unsafe.Pointer(&n)), 1)
	if ok == 0 {
		return 0, e
	}
	return n, nil
}

func (d *winHID) begin() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	d.busy.Add(1)
	return true
}

func (d *winHID) ReadReport() ([]byte, error) {
	if !d.begin() {
		return nil, errors.New("closed")
	}
	defer d.busy.Done()
	var n uint32
	err := syscall.ReadFile(d.h, d.rd.buf[:], &n, &d.rd.ov)
	n, err = d.wait(&d.rd, err, 0)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), d.rd.buf[:n]...), nil
}

func (d *winHID) WriteReport(r []byte) error {
	if !d.begin() {
		return errors.New("closed")
	}
	defer d.busy.Done()
	copy(d.wr.buf[:], r)
	var n uint32
	err := syscall.WriteFile(d.h, d.wr.buf[:], &n, &d.wr.ov)
	_, err = d.wait(&d.wr, err, 2*time.Second)
	return err
}

// Close cancels a read that is waiting, then lets go of the handle.
func (d *winHID) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()
	_ = syscall.CancelIoEx(d.h, nil)
	d.busy.Wait()
	for _, ev := range []syscall.Handle{d.rd.ov.HEvent, d.wr.ov.HEvent} {
		if ev != 0 {
			_ = syscall.CloseHandle(ev)
		}
	}
	return syscall.CloseHandle(d.h)
}
