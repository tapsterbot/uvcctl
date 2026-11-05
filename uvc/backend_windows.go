package uvc

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

// DirectShow camera controls via IAMVideoProcAmp / IAMCameraControl.
// These are the same knobs the Windows "camera properties" dialog shows,
// and they work while another app is streaming from the camera.

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

var (
	clsidSystemDeviceEnum      = guid{0x62BE5D10, 0x60EB, 0x11d0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidICreateDevEnum          = guid{0x29840822, 0x5B84, 0x11D0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	clsidVideoInputDeviceClass = guid{0x860BB310, 0x5D01, 0x11d0, [8]byte{0xBD, 0x3B, 0x00, 0xA0, 0xC9, 0x11, 0xCE, 0x86}}
	iidIBaseFilter             = guid{0x56a86895, 0x0ad4, 0x11ce, [8]byte{0xb0, 0x3a, 0x00, 0x20, 0xaf, 0x0b, 0xa7, 0x70}}
	iidIPropertyBag            = guid{0x55272A00, 0x42CB, 0x11CE, [8]byte{0x81, 0x35, 0x00, 0xAA, 0x00, 0x4B, 0xB8, 0x51}}
	iidIAMVideoProcAmp         = guid{0xC6E13360, 0x30AC, 0x11d0, [8]byte{0xA1, 0x8C, 0x00, 0xA0, 0xC9, 0x11, 0x89, 0x56}}
	iidIAMCameraControl        = guid{0xC6E13370, 0x30AC, 0x11d0, [8]byte{0xA1, 0x8C, 0x00, 0xA0, 0xC9, 0x11, 0x89, 0x56}}
)

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	oleaut32             = syscall.NewLazyDLL("oleaut32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procVariantClear     = oleaut32.NewProc("VariantClear")
)

const (
	coinitMultithreaded = 0x0
	clsctxInprocServer  = 0x1
	sOK                 = 0
	vtBSTR              = 8

	flagAuto   = 0x1
	flagManual = 0x2

	// vtable slots
	mRelease               = 2
	mQueryInterface        = 0
	mCreateClassEnumerator = 3
	mEnumNext              = 3
	mBindToObject          = 8
	mBindToStorage         = 9
	mPropBagRead           = 3
	mGetRange              = 3
	mSet                   = 4
	mGet                   = 5
)

// A COM object: a pointer to a vtable pointer. Not Go memory.
type comObj struct{ vtbl *[64]uintptr }

//go:uintptrescapes
func (o *comObj) call(method int, args ...uintptr) int32 {
	a := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[method], a...)
	return int32(r)
}

func (o *comObj) release() {
	if o != nil {
		o.call(mRelease)
	}
}

func (o *comObj) query(iid *guid) *comObj {
	var out *comObj
	if o.call(mQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))) != sOK {
		return nil
	}
	return out
}

type variant struct {
	vt   uint16
	_    [3]uint16
	bstr *uint16
	_    uintptr
}

var comOnce sync.Once

// comInit joins the process to COM's multithreaded apartment. A parked
// goroutine holds the init so every other thread is implicitly MTA.
func comInit() {
	comOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			procCoInitializeEx.Call(0, coinitMultithreaded)
			close(ready)
			select {}
		}()
		<-ready
	})
}

func hresultErr(op string, hr int32) error {
	return fmt.Errorf("%s failed (HRESULT 0x%08x)", op, uint32(hr))
}

func utf16ptrString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

func readProp(bag *comObj, name string) string {
	key, _ := syscall.UTF16PtrFromString(name)
	var v variant
	if bag.call(mPropBagRead, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)), 0) != sOK {
		return ""
	}
	s := ""
	if v.vt == vtBSTR {
		s = utf16ptrString(v.bstr)
	}
	procVariantClear.Call(uintptr(unsafe.Pointer(&v)))
	return s
}

var vidPidRe = regexp.MustCompile(`(?i)vid_([0-9a-f]{4}).*pid_([0-9a-f]{4})`)

// eachDevice calls fn for every DirectShow video input device. fn returns
// false to stop. The moniker is released after fn returns.
func eachDevice(fn func(info DeviceInfo, moniker *comObj) bool) error {
	comInit()
	var devEnum *comObj
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidSystemDeviceEnum)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidICreateDevEnum)), uintptr(unsafe.Pointer(&devEnum)))
	if int32(hr) != sOK {
		return hresultErr("CoCreateInstance(SystemDeviceEnum)", int32(hr))
	}
	defer devEnum.release()
	var monikers *comObj
	if r := devEnum.call(mCreateClassEnumerator, uintptr(unsafe.Pointer(&clsidVideoInputDeviceClass)), uintptr(unsafe.Pointer(&monikers)), 0); r != sOK {
		return nil // S_FALSE: no video devices
	}
	defer monikers.release()
	for i := 0; ; i++ {
		var m *comObj
		var fetched uint32
		if monikers.call(mEnumNext, 1, uintptr(unsafe.Pointer(&m)), uintptr(unsafe.Pointer(&fetched))) != sOK || fetched == 0 {
			return nil
		}
		info := DeviceInfo{Index: i}
		var bag *comObj
		if m.call(mBindToStorage, 0, 0, uintptr(unsafe.Pointer(&iidIPropertyBag)), uintptr(unsafe.Pointer(&bag))) == sOK {
			info.Name = readProp(bag, "FriendlyName")
			info.Path = readProp(bag, "DevicePath")
			bag.release()
		}
		if mm := vidPidRe.FindStringSubmatch(info.Path); mm != nil {
			v, _ := strconv.ParseUint(mm[1], 16, 16)
			p, _ := strconv.ParseUint(mm[2], 16, 16)
			info.VendorID, info.ProductID = uint16(v), uint16(p)
		}
		more := fn(info, m)
		m.release()
		if !more {
			return nil
		}
	}
}

func listDevices() ([]DeviceInfo, error) {
	var out []DeviceInfo
	err := eachDevice(func(info DeviceInfo, _ *comObj) bool {
		out = append(out, info)
		return true
	})
	return out, err
}

// iface 0 = IAMVideoProcAmp, 1 = IAMCameraControl.
type dsDef struct {
	iface, prop int
	isAuto      bool // this is the auto flag of prop, not its value
}

var dsDefs = map[string]dsDef{
	"brightness":             {0, 0, false},
	"contrast":               {0, 1, false},
	"hue":                    {0, 2, false},
	"saturation":             {0, 3, false},
	"sharpness":              {0, 4, false},
	"gamma":                  {0, 5, false},
	"white_balance":          {0, 7, false},
	"white_balance_auto":     {0, 7, true},
	"backlight_compensation": {0, 8, false},
	"gain":                   {0, 9, false},
	// KSPROPERTY_VIDEOPROCAMP_POWERLINE_FREQUENCY; not in the DirectShow
	// enum, but ksproxy passes the ID through to the driver.
	"power_line_frequency": {0, 13, false},
	"zoom":                 {1, 3, false},
	"exposure":             {1, 4, false},
	"exposure_auto":        {1, 4, true},
	"focus":                {1, 6, false},
	"focus_auto":           {1, 6, true},
}

type winDriver struct {
	filter *comObj
	ifaces [2]*comObj
}

func openDevice(want DeviceInfo) (driver, error) {
	var d *winDriver
	var openErr error
	err := eachDevice(func(info DeviceInfo, m *comObj) bool {
		if info.Index != want.Index || info.Path != want.Path {
			return true
		}
		var f *comObj
		if hr := m.call(mBindToObject, 0, 0, uintptr(unsafe.Pointer(&iidIBaseFilter)), uintptr(unsafe.Pointer(&f))); hr != sOK {
			openErr = hresultErr("BindToObject", hr)
			return false
		}
		d = &winDriver{filter: f}
		d.ifaces[0] = f.query(&iidIAMVideoProcAmp)
		d.ifaces[1] = f.query(&iidIAMCameraControl)
		return false
	})
	if err != nil {
		return nil, err
	}
	if openErr != nil {
		return nil, openErr
	}
	if d == nil {
		return nil, errors.New("camera disappeared")
	}
	return d, nil
}

func (d *winDriver) close() {
	for i, o := range d.ifaces {
		o.release()
		d.ifaces[i] = nil
	}
	d.filter.release()
	d.filter = nil
}

func (d *winDriver) rng(def dsDef) (min, max, step, dflt, caps int32, err error) {
	o := d.ifaces[def.iface]
	if o == nil {
		return 0, 0, 0, 0, 0, ErrUnsupported
	}
	hr := o.call(mGetRange, uintptr(def.prop),
		uintptr(unsafe.Pointer(&min)), uintptr(unsafe.Pointer(&max)), uintptr(unsafe.Pointer(&step)),
		uintptr(unsafe.Pointer(&dflt)), uintptr(unsafe.Pointer(&caps)))
	if hr != sOK {
		return 0, 0, 0, 0, 0, hresultErr("GetRange", hr)
	}
	return
}

func (d *winDriver) raw(def dsDef) (val, flags int32, err error) {
	o := d.ifaces[def.iface]
	if o == nil {
		return 0, 0, ErrUnsupported
	}
	if hr := o.call(mGet, uintptr(def.prop), uintptr(unsafe.Pointer(&val)), uintptr(unsafe.Pointer(&flags))); hr != sOK {
		return 0, 0, hresultErr("Get", hr)
	}
	return val, flags, nil
}

func (d *winDriver) probe() []Control {
	var out []Control
	for _, m := range known {
		def, ok := dsDefs[m.name]
		if !ok {
			continue
		}
		mn, mx, step, dflt, caps, err := d.rng(def)
		if err != nil {
			continue
		}
		if _, _, err := d.raw(def); err != nil {
			continue
		}
		var ctl Control
		if def.isAuto {
			if caps&flagAuto == 0 {
				continue
			}
			// DirectShow doesn't report the default auto state; cameras
			// ship with auto on.
			ctl = Control{Name: m.name, Type: TypeBool, Min: 0, Max: 1, Default: 1}
		} else {
			ctl = Control{Name: m.name, Type: TypeInt, Min: int(mn), Max: int(mx), Step: int(step), Default: int(dflt)}
			switch m.name {
			case "power_line_frequency":
				ctl.Type, ctl.Step = TypeMenu, 0
			case "exposure":
				ctl.Unit = "log2 s" // -6 = 1/64 s, -7 = 1/128 s ...
			case "white_balance":
				ctl.Unit = "K"
			}
		}
		if v, err := d.get(m.name); err == nil {
			ctl.Value = v
		}
		out = append(out, ctl)
	}
	return out
}

func (d *winDriver) get(name string) (int, error) {
	def, ok := dsDefs[name]
	if !ok {
		return 0, ErrUnsupported
	}
	val, flags, err := d.raw(def)
	if err != nil {
		return 0, err
	}
	if def.isAuto {
		return boolInt(flags&flagAuto != 0), nil
	}
	return int(val), nil
}

func (d *winDriver) set(name string, v int) error {
	def, ok := dsDefs[name]
	if !ok {
		return ErrUnsupported
	}
	o := d.ifaces[def.iface]
	if o == nil {
		return ErrUnsupported
	}
	val, flags := int32(v), int32(flagManual)
	if def.isAuto {
		cur, _, err := d.raw(def)
		if err != nil {
			return err
		}
		val = cur
		if v != 0 {
			flags = flagAuto
		}
	}
	if hr := o.call(mSet, uintptr(def.prop), uintptr(val), uintptr(flags)); hr != sOK {
		return hresultErr("Set", hr)
	}
	return nil
}
