// Package uvc reads and writes USB camera (UVC) image controls — exposure,
// white balance, gain, focus, brightness and friends — on macOS (IOKit),
// Linux (V4L2) and Windows (DirectShow).
//
// Control names are the same on every platform. Value ranges come from the
// camera itself, so always check Control.Min/Max. Exposure units differ by
// platform (see Control.Unit).
//
//	cam, err := uvc.Open("")             // first camera
//	defer cam.Close()
//	cam.Apply(map[string]int{            // safe ordering: autos off first
//	    "exposure_auto": 0, "exposure": 150,
//	    "white_balance_auto": 0, "white_balance": 4600,
//	})
package uvc

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Control types.
const (
	TypeInt  = "int"
	TypeBool = "bool"
	TypeMenu = "menu"
)

// Control describes one camera control and its current value.
type Control struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Min     int    `json:"min"`
	Max     int    `json:"max"`
	Step    int    `json:"step,omitempty"`
	Default int    `json:"default"`
	Value   int    `json:"value"`
	Unit    string `json:"unit,omitempty"`
	// Auto names the toggle that, when on, makes the camera drive this
	// control itself (e.g. "exposure" is governed by "exposure_auto").
	Auto string `json:"auto,omitempty"`
}

// DeviceInfo identifies a camera.
type DeviceInfo struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	VendorID  uint16 `json:"vendor_id"`
	ProductID uint16 `json:"product_id"`
	Path      string `json:"path"`
}

// ID returns "vvvv:pppp", or "" when the platform couldn't tell.
func (d DeviceInfo) ID() string {
	if d.VendorID == 0 && d.ProductID == 0 {
		return ""
	}
	return fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)
}

// ErrUnsupported is returned for controls the camera doesn't expose.
var ErrUnsupported = errors.New("control not supported by this camera")

// ErrNoCamera is returned when no camera matches.
var ErrNoCamera = errors.New("no matching camera found")

type meta struct {
	name, label, auto string
}

// Every control this package knows, in display order. Auto toggles come
// right before the value they govern.
var known = []meta{
	{"exposure_auto", "Auto exposure", ""},
	{"exposure", "Exposure time", "exposure_auto"},
	{"gain", "Gain", ""},
	{"white_balance_auto", "Auto white balance", ""},
	{"white_balance", "White balance temperature", "white_balance_auto"},
	{"focus_auto", "Autofocus", ""},
	{"focus", "Focus", "focus_auto"},
	{"brightness", "Brightness", ""},
	{"contrast", "Contrast", ""},
	{"saturation", "Saturation", ""},
	{"hue", "Hue", ""},
	{"sharpness", "Sharpness", ""},
	{"gamma", "Gamma", ""},
	{"backlight_compensation", "Backlight compensation", ""},
	{"power_line_frequency", "Anti-flicker (0=off 1=50Hz 2=60Hz)", ""},
	{"zoom", "Zoom", ""},
}

var aliases = map[string]string{
	"auto_exposure":              "exposure_auto",
	"exposure_absolute":          "exposure",
	"exposure_time_absolute":     "exposure",
	"auto_white_balance":         "white_balance_auto",
	"white_balance_temperature":  "white_balance",
	"white_balance_automatic":    "white_balance_auto",
	"wb":                         "white_balance",
	"wb_auto":                    "white_balance_auto",
	"auto_focus":                 "focus_auto",
	"autofocus":                  "focus_auto",
	"focus_absolute":             "focus",
	"focus_automatic_continuous": "focus_auto",
	"zoom_absolute":              "zoom",
	"power_line":                 "power_line_frequency",
	"anti_flicker":               "power_line_frequency",
	"backlight":                  "backlight_compensation",
}

// CanonicalName maps aliases (e.g. v4l2-ctl names like "exposure_absolute")
// to this package's control names.
func CanonicalName(name string) string {
	n := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	if a, ok := aliases[n]; ok {
		return a
	}
	return n
}

func lookupMeta(name string) meta {
	for _, m := range known {
		if m.name == name {
			return m
		}
	}
	return meta{name: name, label: name}
}

// driver is the per-platform backend.
type driver interface {
	// probe returns supported controls with ranges and current values.
	probe() []Control
	get(name string) (int, error)
	set(name string, v int) error
	close()
}

// Camera is an open camera.
type Camera struct {
	info   DeviceInfo
	drv    driver
	ctrls  []Control
	byName map[string]int
}

// Devices lists connected cameras.
func Devices() ([]DeviceInfo, error) { return listDevices() }

// Open opens a camera. selector may be "" (first camera), an index from
// Devices, a "vvvv:pppp" USB ID, a device path, or part of the name.
func Open(selector string) (*Camera, error) {
	devs, err := listDevices()
	if err != nil {
		return nil, err
	}
	d, err := pick(devs, selector)
	if err != nil {
		return nil, err
	}
	drv, err := openDevice(d)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", d.Name, err)
	}
	c := &Camera{info: d, drv: drv, byName: map[string]int{}}
	ctrls := drv.probe()
	order := map[string]int{}
	for i, m := range known {
		order[m.name] = i
	}
	sort.SliceStable(ctrls, func(i, j int) bool { return order[ctrls[i].Name] < order[ctrls[j].Name] })
	for i := range ctrls {
		m := lookupMeta(ctrls[i].Name)
		if ctrls[i].Label == "" {
			ctrls[i].Label = m.label
		}
		ctrls[i].Auto = m.auto
		c.byName[ctrls[i].Name] = i
	}
	// Only link to an auto toggle the camera actually has.
	for i := range ctrls {
		if _, ok := c.byName[ctrls[i].Auto]; !ok {
			ctrls[i].Auto = ""
		}
	}
	c.ctrls = ctrls
	return c, nil
}

var idRe = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{4}$`)

func pick(devs []DeviceInfo, sel string) (DeviceInfo, error) {
	if len(devs) == 0 {
		return DeviceInfo{}, ErrNoCamera
	}
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return devs[0], nil
	}
	if n, err := strconv.Atoi(sel); err == nil {
		for _, d := range devs {
			if d.Index == n {
				return d, nil
			}
		}
		return DeviceInfo{}, fmt.Errorf("%w: no camera at index %d", ErrNoCamera, n)
	}
	if idRe.MatchString(sel) {
		for _, d := range devs {
			if strings.EqualFold(d.ID(), sel) {
				return d, nil
			}
		}
		return DeviceInfo{}, fmt.Errorf("%w: no camera with USB ID %s", ErrNoCamera, sel)
	}
	for _, d := range devs {
		if d.Path != "" && d.Path == sel {
			return d, nil
		}
	}
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(sel)) {
			return d, nil
		}
	}
	return DeviceInfo{}, fmt.Errorf("%w: nothing matches %q", ErrNoCamera, sel)
}

// Info describes the open camera.
func (c *Camera) Info() DeviceInfo { return c.info }

// Close releases the camera.
func (c *Camera) Close() error {
	if c.drv != nil {
		c.drv.close()
		c.drv = nil
	}
	return nil
}

// Controls returns every supported control with a fresh current value.
func (c *Camera) Controls() []Control {
	out := make([]Control, len(c.ctrls))
	copy(out, c.ctrls)
	for i := range out {
		if v, err := c.drv.get(out[i].Name); err == nil {
			out[i].Value = v
		}
	}
	return out
}

// Control returns one control's description (with a fresh value).
func (c *Camera) Control(name string) (Control, bool) {
	i, ok := c.byName[CanonicalName(name)]
	if !ok {
		return Control{}, false
	}
	ctl := c.ctrls[i]
	if v, err := c.drv.get(ctl.Name); err == nil {
		ctl.Value = v
	}
	return ctl, true
}

// Get reads a control's current value.
func (c *Camera) Get(name string) (int, error) {
	name = CanonicalName(name)
	if _, ok := c.byName[name]; !ok {
		return 0, fmt.Errorf("%s: %w", name, ErrUnsupported)
	}
	return c.drv.get(name)
}

// Set writes one control. For a value governed by an auto mode (exposure,
// white_balance, focus), turn the auto mode off first — or use Apply,
// which does that for you.
func (c *Camera) Set(name string, v int) error {
	name = CanonicalName(name)
	i, ok := c.byName[name]
	if !ok {
		return fmt.Errorf("%s: %w", name, ErrUnsupported)
	}
	ctl := c.ctrls[i]
	if v < ctl.Min || v > ctl.Max {
		return fmt.Errorf("%s: %d is out of range (%d..%d)", name, v, ctl.Min, ctl.Max)
	}
	if err := c.drv.set(name, v); err != nil {
		return fmt.Errorf("%s=%d: %w", name, v, err)
	}
	return nil
}

// isAuto reports whether name is an auto-mode toggle (e.g. exposure_auto).
func (c *Camera) isAuto(name string) bool {
	for _, m := range known {
		if m.auto == name {
			return true
		}
	}
	return false
}

// Apply writes several controls in an order cameras accept:
//  1. auto modes being turned off,
//  2. plain values (skipping ones an auto mode will override anyway),
//  3. auto modes being turned on.
//
// Unknown or unsupported names are reported in the returned error but don't
// stop the rest from being applied.
func (c *Camera) Apply(values map[string]int) error {
	want := map[string]int{}
	var errs []error
	for k, v := range values {
		n := CanonicalName(k)
		if _, ok := c.byName[n]; !ok {
			errs = append(errs, fmt.Errorf("%s: %w", n, ErrUnsupported))
			continue
		}
		want[n] = v
	}
	autoOn := func(name string) bool {
		if v, ok := want[name]; ok {
			return v != 0
		}
		v, err := c.drv.get(name)
		return err == nil && v != 0
	}
	ordered := func(keep func(name string, v int) bool) {
		for _, ctl := range c.ctrls {
			if v, ok := want[ctl.Name]; ok && keep(ctl.Name, v) {
				if err := c.Set(ctl.Name, v); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	ordered(func(n string, v int) bool { return c.isAuto(n) && v == 0 })
	ordered(func(n string, v int) bool {
		if c.isAuto(n) {
			return false
		}
		ctl := c.ctrls[c.byName[n]]
		return ctl.Auto == "" || !autoOn(ctl.Auto)
	})
	ordered(func(n string, v int) bool { return c.isAuto(n) && v != 0 })
	return errors.Join(errs...)
}

// Snapshot returns the current value of every control, suitable for Apply.
// Values that an active auto mode is driving are left out, since writing
// them back would be ignored.
func (c *Camera) Snapshot() map[string]int {
	ctrls := c.Controls()
	cur := map[string]int{}
	for _, ctl := range ctrls {
		cur[ctl.Name] = ctl.Value
	}
	out := map[string]int{}
	for _, ctl := range ctrls {
		if ctl.Auto != "" && cur[ctl.Auto] != 0 {
			continue
		}
		out[ctl.Name] = ctl.Value
	}
	return out
}

// Lock turns on auto exposure, auto white balance and autofocus, waits for
// the camera to settle, then turns the autos off. The camera keeps the
// values its autos chose, so the image stops changing.
//
// The camera must be streaming video (some app has it open); auto modes
// don't run otherwise.
//
// Lock returns the names of controls whose reported value never changed
// while the autos ran. Some cameras (e.g. Arducam 16MP) keep reporting the
// last manual value instead of what auto picked: the image is still
// locked correctly, but reading those values back (or saving them) gives
// stale numbers.
func (c *Camera) Lock(settle time.Duration) (unreported []string, err error) {
	autos := []string{"exposure_auto", "white_balance_auto", "focus_auto"}
	on := map[string]int{}
	for _, a := range autos {
		if _, ok := c.byName[a]; ok {
			on[a] = 1
		}
	}
	if len(on) == 0 {
		return nil, errors.New("camera has no auto modes to settle")
	}
	before := map[string]int{}
	for _, ctl := range c.Controls() {
		before[ctl.Name] = ctl.Value
	}
	if err := c.Apply(on); err != nil {
		return nil, err
	}
	time.Sleep(settle)
	for _, ctl := range c.Controls() {
		if ctl.Auto != "" && on[ctl.Auto] == 1 && ctl.Value == before[ctl.Name] {
			unreported = append(unreported, ctl.Name)
		}
	}
	// Don't write values back: on cameras that don't report what auto
	// picked, that would replace the good values with stale ones.
	off := map[string]int{}
	for a := range on {
		off[a] = 0
	}
	return unreported, c.Apply(off)
}

// Reset sets every control, auto modes included, to the camera's default.
func (c *Camera) Reset() error {
	// Write default values with the autos off (so the camera accepts them,
	// and they're what it falls back to later), then restore the autos.
	vals, autos := map[string]int{}, map[string]int{}
	for _, ctl := range c.ctrls {
		if c.isAuto(ctl.Name) {
			vals[ctl.Name] = 0
			autos[ctl.Name] = ctl.Default
		} else {
			vals[ctl.Name] = ctl.Default
		}
	}
	return errors.Join(c.Apply(vals), c.Apply(autos))
}

// ParseValue turns user input into a value for this control. It accepts
// numbers, on/off/auto/manual for toggles, 50hz/60hz/off for
// power_line_frequency, and default/min/max for anything.
func (ctl Control) ParseValue(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "default":
		return ctl.Default, nil
	case "min":
		return ctl.Min, nil
	case "max":
		return ctl.Max, nil
	}
	if ctl.Type == TypeBool {
		switch s {
		case "on", "true", "yes", "auto":
			return 1, nil
		case "off", "false", "no", "manual":
			return 0, nil
		}
	}
	if ctl.Name == "power_line_frequency" {
		switch s {
		case "off", "disabled":
			return 0, nil
		case "50", "50hz":
			return 1, nil
		case "60", "60hz":
			return 2, nil
		case "auto":
			return 3, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: can't parse %q", ctl.Name, s)
	}
	return v, nil
}

// FormatValue renders a value the way a person would type it.
func (ctl Control) FormatValue(v int) string {
	if ctl.Type == TypeBool {
		if v != 0 {
			return "on"
		}
		return "off"
	}
	if ctl.Name == "power_line_frequency" {
		switch v {
		case 0:
			return "off"
		case 1:
			return "50hz"
		case 2:
			return "60hz"
		case 3:
			return "auto"
		}
	}
	return strconv.Itoa(v)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
