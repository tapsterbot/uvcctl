package uvc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// V4L2 ioctls (linux/videodev2.h).
const (
	vidiocQueryCap  = 0x80685600
	vidiocGCtrl     = 0xc008561b
	vidiocSCtrl     = 0xc008561c
	vidiocQueryCtrl = 0xc0445624
	vidiocQueryMenu = 0xc02c5625

	capVideoCapture      = 0x00000001
	capVideoCaptureMPane = 0x00001000
	capDeviceCaps        = 0x80000000

	ctrlFlagDisabled = 0x0001
	ctrlTypeBoolean  = 2
	ctrlTypeMenu     = 3

	cidBase   = 0x00980900
	cidCamera = 0x009a0900

	exposureAuto     = 0
	exposureManual   = 1
	exposureShutter  = 2
	exposureAperture = 3
)

var v4l2IDs = map[string]uint32{
	"brightness":             cidBase + 0,
	"contrast":               cidBase + 1,
	"saturation":             cidBase + 2,
	"hue":                    cidBase + 3,
	"white_balance_auto":     cidBase + 12,
	"gamma":                  cidBase + 16,
	"gain":                   cidBase + 19,
	"power_line_frequency":   cidBase + 24,
	"white_balance":          cidBase + 26,
	"sharpness":              cidBase + 27,
	"backlight_compensation": cidBase + 28,
	"exposure_auto":          cidCamera + 1,
	"exposure":               cidCamera + 2,
	"focus":                  cidCamera + 10,
	"focus_auto":             cidCamera + 12,
	"zoom":                   cidCamera + 13,
}

type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

type v4l2QueryCtrl struct {
	ID           uint32
	Type         uint32
	Name         [32]byte
	Minimum      int32
	Maximum      int32
	Step         int32
	DefaultValue int32
	Flags        uint32
	Reserved     [2]uint32
}

type v4l2Control struct {
	ID    uint32
	Value int32
}

type v4l2QueryMenu struct {
	ID       uint32
	Index    uint32
	Name     [32]byte
	Reserved uint32
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func listDevices() ([]DeviceInfo, error) {
	paths, _ := filepath.Glob("/dev/video*")
	num := func(p string) int { n, _ := strconv.Atoi(strings.TrimPrefix(p, "/dev/video")); return n }
	sort.Slice(paths, func(i, j int) bool { return num(paths[i]) < num(paths[j]) })
	var out []DeviceInfo
	for _, p := range paths {
		fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		var cap v4l2Capability
		err = ioctl(fd, vidiocQueryCap, unsafe.Pointer(&cap))
		syscall.Close(fd)
		if err != nil {
			continue
		}
		caps := cap.Capabilities
		if caps&capDeviceCaps != 0 {
			caps = cap.DeviceCaps
		}
		// UVC cameras create a second /dev/video node for metadata; skip it,
		// along with non-USB nodes (ISPs, codecs).
		if cstr(cap.Driver[:]) != "uvcvideo" || caps&(capVideoCapture|capVideoCaptureMPane) == 0 {
			continue
		}
		d := DeviceInfo{Index: len(out), Name: cstr(cap.Card[:]), Path: p}
		d.VendorID, d.ProductID = sysfsUSBID(filepath.Base(p))
		out = append(out, d)
	}
	return out, nil
}

// sysfsUSBID reads idVendor/idProduct for /dev/videoN from sysfs.
func sysfsUSBID(node string) (uint16, uint16) {
	dir, err := filepath.EvalSymlinks("/sys/class/video4linux/" + node + "/device")
	if err != nil {
		return 0, 0
	}
	for i := 0; i < 3 && dir != "/"; i++ {
		v, err1 := os.ReadFile(filepath.Join(dir, "idVendor"))
		p, err2 := os.ReadFile(filepath.Join(dir, "idProduct"))
		if err1 == nil && err2 == nil {
			vv, _ := strconv.ParseUint(strings.TrimSpace(string(v)), 16, 16)
			pp, _ := strconv.ParseUint(strings.TrimSpace(string(p)), 16, 16)
			return uint16(vv), uint16(pp)
		}
		dir = filepath.Dir(dir)
	}
	return 0, 0
}

type linuxDriver struct {
	fd       int
	aeOnMode int32
}

func openDevice(d DeviceInfo) (driver, error) {
	fd, err := syscall.Open(d.Path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("%w (add your user to the 'video' group, or run with sudo)", err)
		}
		return nil, err
	}
	return &linuxDriver{fd: fd, aeOnMode: exposureAperture}, nil
}

func (d *linuxDriver) close() {
	if d.fd >= 0 {
		syscall.Close(d.fd)
		d.fd = -1
	}
}

func (d *linuxDriver) menuHas(id uint32, idx uint32) bool {
	m := v4l2QueryMenu{ID: id, Index: idx}
	return ioctl(d.fd, vidiocQueryMenu, unsafe.Pointer(&m)) == nil
}

func (d *linuxDriver) probe() []Control {
	var out []Control
	for _, m := range known {
		id, ok := v4l2IDs[m.name]
		if !ok {
			continue
		}
		q := v4l2QueryCtrl{ID: id}
		if ioctl(d.fd, vidiocQueryCtrl, unsafe.Pointer(&q)) != nil || q.Flags&ctrlFlagDisabled != 0 {
			continue
		}
		ctl := Control{
			Name: m.name, Type: TypeInt,
			Min: int(q.Minimum), Max: int(q.Maximum), Step: int(q.Step), Default: int(q.DefaultValue),
		}
		switch {
		case m.name == "exposure_auto":
			// V4L2 menu: 0=auto 1=manual 2=shutter priority 3=aperture priority.
			ctl.Type, ctl.Min, ctl.Max, ctl.Step = TypeBool, 0, 1, 0
			ctl.Default = boolInt(q.DefaultValue != exposureManual)
			for _, mode := range []int32{exposureAperture, exposureAuto, exposureShutter} {
				if d.menuHas(id, uint32(mode)) {
					d.aeOnMode = mode
					break
				}
			}
		case q.Type == ctrlTypeBoolean:
			ctl.Type, ctl.Step = TypeBool, 0
		case q.Type == ctrlTypeMenu:
			ctl.Type, ctl.Step = TypeMenu, 0
		}
		switch m.name {
		case "exposure":
			ctl.Unit = "100µs"
		case "white_balance":
			ctl.Unit = "K"
		}
		if v, err := d.get(m.name); err == nil {
			ctl.Value = v
		}
		out = append(out, ctl)
	}
	return out
}

func (d *linuxDriver) get(name string) (int, error) {
	id, ok := v4l2IDs[name]
	if !ok {
		return 0, ErrUnsupported
	}
	c := v4l2Control{ID: id}
	if err := ioctl(d.fd, vidiocGCtrl, unsafe.Pointer(&c)); err != nil {
		return 0, err
	}
	if name == "exposure_auto" {
		return boolInt(c.Value != exposureManual), nil
	}
	return int(c.Value), nil
}

func (d *linuxDriver) set(name string, v int) error {
	id, ok := v4l2IDs[name]
	if !ok {
		return ErrUnsupported
	}
	val := int32(v)
	if name == "exposure_auto" {
		val = exposureManual
		if v != 0 {
			val = d.aeOnMode
		}
	}
	c := v4l2Control{ID: id, Value: val}
	err := ioctl(d.fd, vidiocSCtrl, unsafe.Pointer(&c))
	if errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("%w (is the matching auto mode still on?)", err)
	}
	return err
}
