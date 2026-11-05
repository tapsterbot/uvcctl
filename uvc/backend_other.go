//go:build !darwin && !linux && !windows

package uvc

import (
	"errors"
	"runtime"
)

var errPlatform = errors.New("uvc: camera controls are not supported on " + runtime.GOOS)

func listDevices() ([]DeviceInfo, error)      { return nil, errPlatform }
func openDevice(d DeviceInfo) (driver, error) { return nil, errPlatform }
