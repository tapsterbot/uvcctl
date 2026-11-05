package uvc

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include "uvc_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// UVC request codes.
const (
	reqSetCur = 0x01
	reqGetCur = 0x81
	reqGetMin = 0x82
	reqGetMax = 0x83
	reqGetRes = 0x84
	reqGetDef = 0x87
)

const (
	unitCT = iota // Camera Terminal
	unitPU        // Processing Unit
)

type uvcDef struct {
	unit, selector, size int
	signed               bool
	typ                  string
}

// UVC 1.5 spec, tables 4-12 (camera terminal) and 4-13 (processing unit).
var uvcDefs = map[string]uvcDef{
	"exposure_auto":          {unitCT, 0x02, 1, false, TypeBool},
	"exposure":               {unitCT, 0x04, 4, false, TypeInt},
	"focus":                  {unitCT, 0x06, 2, false, TypeInt},
	"focus_auto":             {unitCT, 0x08, 1, false, TypeBool},
	"zoom":                   {unitCT, 0x0B, 2, false, TypeInt},
	"backlight_compensation": {unitPU, 0x01, 2, false, TypeInt},
	"brightness":             {unitPU, 0x02, 2, true, TypeInt},
	"contrast":               {unitPU, 0x03, 2, false, TypeInt},
	"gain":                   {unitPU, 0x04, 2, false, TypeInt},
	"power_line_frequency":   {unitPU, 0x05, 1, false, TypeMenu},
	"hue":                    {unitPU, 0x06, 2, true, TypeInt},
	"saturation":             {unitPU, 0x07, 2, false, TypeInt},
	"sharpness":              {unitPU, 0x08, 2, false, TypeInt},
	"gamma":                  {unitPU, 0x09, 2, false, TypeInt},
	"white_balance":          {unitPU, 0x0A, 2, false, TypeInt},
	"white_balance_auto":     {unitPU, 0x0B, 1, false, TypeBool},
}

func listDevices() ([]DeviceInfo, error) {
	var buf [32]C.uvcx_info
	n := int(C.uvcx_list(&buf[0], C.int(len(buf))))
	out := make([]DeviceInfo, 0, n)
	for i := 0; i < n; i++ {
		b := buf[i]
		out = append(out, DeviceInfo{
			Index:     i,
			Name:      C.GoString(&b.name[0]),
			VendorID:  uint16(b.vid),
			ProductID: uint16(b.pid),
			Path:      fmt.Sprintf("usb:0x%08x", uint32(b.location)),
		})
	}
	return out, nil
}

type darwinDriver struct {
	h        *C.uvcx_handle
	ct, pu   int
	aeOnMode int // which UVC auto-exposure mode means "on" for this camera
}

func openDevice(d DeviceInfo) (driver, error) {
	var loc uint32
	if _, err := fmt.Sscanf(d.Path, "usb:0x%x", &loc); err != nil {
		return nil, err
	}
	h := C.uvcx_open(C.uint32_t(loc))
	if h == nil {
		return nil, errors.New("device not found or has no UVC control interface")
	}
	return &darwinDriver{
		h:  h,
		ct: int(C.uvcx_camera_terminal(h)),
		pu: int(C.uvcx_processing_unit(h)),
	}, nil
}

func (d *darwinDriver) close() {
	if d.h != nil {
		C.uvcx_close(d.h)
		d.h = nil
	}
}

func (d *darwinDriver) raw(req int, def uvcDef) (int, error) {
	if d.h == nil {
		return 0, errors.New("camera closed")
	}
	var buf [8]byte
	unit := d.ct
	if def.unit == unitPU {
		unit = d.pu
	}
	rc := C.uvcx_xfer(d.h, C.int(req), C.int(unit), C.int(def.selector), C.int(def.size), (*C.uint8_t)(unsafe.Pointer(&buf[0])))
	if rc != C.UVCX_OK {
		return 0, fmt.Errorf("UVC request 0x%02x failed (camera unplugged or control unsupported)", req)
	}
	var u uint32
	for i := 0; i < def.size; i++ {
		u |= uint32(buf[i]) << (8 * i)
	}
	if def.signed {
		switch def.size {
		case 1:
			return int(int8(u)), nil
		case 2:
			return int(int16(u)), nil
		}
		return int(int32(u)), nil
	}
	return int(u), nil
}

func (d *darwinDriver) probe() []Control {
	var out []Control
	for _, m := range known {
		def, ok := uvcDefs[m.name]
		if !ok {
			continue
		}
		cur, err := d.raw(reqGetCur, def)
		if err != nil {
			continue
		}
		ctl := Control{Name: m.name, Type: def.typ, Value: cur}
		switch {
		case m.name == "exposure_auto":
			// UVC auto-exposure is a mode bitmap: 1=manual, 2=auto,
			// 4=shutter priority, 8=aperture priority. GET_RES says which
			// modes the camera supports; pick the best one for "on".
			d.aeOnMode = 8
			if res, err := d.raw(reqGetRes, def); err == nil {
				switch {
				case res&8 != 0:
					d.aeOnMode = 8
				case res&2 != 0:
					d.aeOnMode = 2
				case res&4 != 0:
					d.aeOnMode = 4
				}
			}
			ctl.Min, ctl.Max, ctl.Default = 0, 1, 1
			if v, err := d.raw(reqGetDef, def); err == nil {
				ctl.Default = boolInt(v != 1)
			}
			ctl.Value = boolInt(cur != 1)
		case def.typ == TypeBool:
			ctl.Min, ctl.Max, ctl.Default = 0, 1, 1
			if v, err := d.raw(reqGetDef, def); err == nil {
				ctl.Default = boolInt(v != 0)
			}
			ctl.Value = boolInt(cur != 0)
		case def.typ == TypeMenu:
			ctl.Min, ctl.Max = 0, 2
			if v, err := d.raw(reqGetMin, def); err == nil {
				ctl.Min = v
			}
			if v, err := d.raw(reqGetMax, def); err == nil {
				ctl.Max = v
			}
			ctl.Default = cur
			if v, err := d.raw(reqGetDef, def); err == nil {
				ctl.Default = v
			}
		default:
			mn, err1 := d.raw(reqGetMin, def)
			mx, err2 := d.raw(reqGetMax, def)
			if err1 != nil || err2 != nil {
				continue
			}
			ctl.Min, ctl.Max, ctl.Default = mn, mx, cur
			if v, err := d.raw(reqGetDef, def); err == nil {
				ctl.Default = v
			}
			if v, err := d.raw(reqGetRes, def); err == nil && v > 0 {
				ctl.Step = v
			}
			switch m.name {
			case "exposure":
				ctl.Unit = "100µs"
			case "white_balance":
				ctl.Unit = "K"
			}
		}
		out = append(out, ctl)
	}
	return out
}

func (d *darwinDriver) get(name string) (int, error) {
	def, ok := uvcDefs[name]
	if !ok {
		return 0, ErrUnsupported
	}
	v, err := d.raw(reqGetCur, def)
	if err != nil {
		return 0, err
	}
	switch {
	case name == "exposure_auto":
		return boolInt(v != 1), nil
	case def.typ == TypeBool:
		return boolInt(v != 0), nil
	}
	return v, nil
}

func (d *darwinDriver) set(name string, v int) error {
	def, ok := uvcDefs[name]
	if !ok {
		return ErrUnsupported
	}
	if name == "exposure_auto" {
		if v == 0 {
			v = 1 // manual
		} else if d.aeOnMode != 0 {
			v = d.aeOnMode
		} else {
			v = 8 // aperture priority: the usual webcam "auto"
		}
	}
	if d.h == nil {
		return errors.New("camera closed")
	}
	var buf [8]byte
	for i := 0; i < def.size; i++ {
		buf[i] = byte(uint32(v) >> (8 * i))
	}
	unit := d.ct
	if def.unit == unitPU {
		unit = d.pu
	}
	rc := C.uvcx_xfer(d.h, reqSetCur, C.int(unit), C.int(def.selector), C.int(def.size), (*C.uint8_t)(unsafe.Pointer(&buf[0])))
	if rc != C.UVCX_OK {
		return errors.New("camera rejected the value")
	}
	return nil
}
