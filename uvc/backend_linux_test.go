package uvc

import (
	"testing"
	"unsafe"
)

// Sizes must match linux/videodev2.h, since they're baked into the ioctl numbers.
func TestV4L2StructSizes(t *testing.T) {
	for name, got := range map[string]uintptr{
		"v4l2_capability": unsafe.Sizeof(v4l2Capability{}),
		"v4l2_queryctrl":  unsafe.Sizeof(v4l2QueryCtrl{}),
		"v4l2_control":    unsafe.Sizeof(v4l2Control{}),
		"v4l2_querymenu":  unsafe.Sizeof(v4l2QueryMenu{}),
	} {
		want := map[string]uintptr{"v4l2_capability": 104, "v4l2_queryctrl": 68, "v4l2_control": 8, "v4l2_querymenu": 44}[name]
		if got != want {
			t.Errorf("%s: size %d, want %d", name, got, want)
		}
		// ioctl encodes the size in bits 16..29
		ioc := map[string]uintptr{"v4l2_capability": vidiocQueryCap, "v4l2_queryctrl": vidiocQueryCtrl, "v4l2_control": vidiocGCtrl, "v4l2_querymenu": vidiocQueryMenu}[name]
		if (ioc>>16)&0x3fff != want {
			t.Errorf("%s: ioctl size field %d, want %d", name, (ioc>>16)&0x3fff, want)
		}
	}
}
