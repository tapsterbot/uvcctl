package uvc

import (
	"errors"
	"testing"
	"time"
)

// fakeCam behaves like a strict UVC camera: writing a value while its auto
// mode is on fails (as Linux uvcvideo does with EACCES), and autos move
// their values on their own.
type fakeCam struct {
	vals map[string]int
	log  []string
}

var fakeAuto = map[string]string{"exposure": "exposure_auto", "white_balance": "white_balance_auto"}

func newFake() *fakeCam {
	return &fakeCam{vals: map[string]int{
		"exposure_auto": 1, "exposure": 300,
		"white_balance_auto": 1, "white_balance": 5000,
		"gain": 10, "brightness": 0,
	}}
}

func (f *fakeCam) probe() []Control {
	return []Control{
		{Name: "white_balance", Type: TypeInt, Min: 2800, Max: 6500, Default: 4600},
		{Name: "brightness", Type: TypeInt, Min: -64, Max: 64},
		{Name: "exposure", Type: TypeInt, Min: 1, Max: 5000, Default: 157},
		{Name: "exposure_auto", Type: TypeBool, Min: 0, Max: 1, Default: 1},
		{Name: "gain", Type: TypeInt, Min: 0, Max: 100},
		{Name: "white_balance_auto", Type: TypeBool, Min: 0, Max: 1, Default: 1},
	}
}
func (f *fakeCam) get(n string) (int, error) {
	v, ok := f.vals[n]
	if !ok {
		return 0, ErrUnsupported
	}
	return v, nil
}
func (f *fakeCam) set(n string, v int) error {
	if a, ok := fakeAuto[n]; ok && f.vals[a] != 0 {
		return errors.New("inactive: auto mode on")
	}
	f.vals[n] = v
	f.log = append(f.log, n)
	return nil
}
func (f *fakeCam) close() {}

func openFake(f *fakeCam) *Camera {
	c := &Camera{drv: f, byName: map[string]int{}}
	ctrls := f.probe()
	// mimic Open's ordering/linking
	order := map[string]int{}
	for i, m := range known {
		order[m.name] = i
	}
	for i := 1; i < len(ctrls); i++ {
		for j := i; j > 0 && order[ctrls[j].Name] < order[ctrls[j-1].Name]; j-- {
			ctrls[j], ctrls[j-1] = ctrls[j-1], ctrls[j]
		}
	}
	for i := range ctrls {
		ctrls[i].Auto = lookupMeta(ctrls[i].Name).auto
		c.byName[ctrls[i].Name] = i
	}
	c.ctrls = ctrls
	return c
}

func TestApplyTurnsAutosOffFirst(t *testing.T) {
	f := newFake()
	c := openFake(f)
	err := c.Apply(map[string]int{"exposure": 150, "exposure_auto": 0, "white_balance": 4000, "white_balance_auto": 0, "gain": 5})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]int{"exposure": 150, "exposure_auto": 0, "white_balance": 4000, "white_balance_auto": 0, "gain": 5} {
		if f.vals[k] != want {
			t.Errorf("%s = %d, want %d", k, f.vals[k], want)
		}
	}
}

func TestApplySkipsValuesDrivenByAuto(t *testing.T) {
	f := newFake()
	c := openFake(f)
	// exposure_auto stays on, so writing exposure would just fail.
	if err := c.Apply(map[string]int{"exposure": 150, "gain": 5}); err != nil {
		t.Fatal(err)
	}
	if f.vals["exposure"] != 300 {
		t.Errorf("exposure was written while auto on")
	}
}

func TestApplyTurnsAutosOnLast(t *testing.T) {
	f := newFake()
	f.vals["exposure_auto"] = 0
	c := openFake(f)
	if err := c.Apply(map[string]int{"exposure_auto": 1, "exposure": 150}); err != nil {
		t.Fatal(err)
	}
	if f.vals["exposure_auto"] != 1 {
		t.Error("auto not turned on")
	}
}

func TestApplyReportsUnsupportedButContinues(t *testing.T) {
	f := newFake()
	c := openFake(f)
	err := c.Apply(map[string]int{"zoom": 3, "gain": 7})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
	if f.vals["gain"] != 7 {
		t.Error("gain not applied")
	}
}

func TestLockFreezesAutoValues(t *testing.T) {
	f := newFake()
	f.vals["exposure_auto"], f.vals["white_balance_auto"] = 0, 0
	c := openFake(f)
	unreported, err := c.Lock(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if f.vals["exposure_auto"] != 0 || f.vals["white_balance_auto"] != 0 {
		t.Error("autos left on")
	}
	// The fake never changes values on its own, so both look unreported,
	// and Lock must not have written anything but the auto toggles.
	if len(unreported) != 2 {
		t.Errorf("unreported = %v", unreported)
	}
	for _, n := range f.log {
		if n != "exposure_auto" && n != "white_balance_auto" {
			t.Errorf("Lock wrote %s", n)
		}
	}
}

func TestSnapshotOmitsAutoDrivenValues(t *testing.T) {
	c := openFake(newFake())
	s := c.Snapshot()
	if _, ok := s["exposure"]; ok {
		t.Error("snapshot includes exposure while auto on")
	}
	if s["exposure_auto"] != 1 || s["gain"] != 10 {
		t.Errorf("bad snapshot %v", s)
	}
}

func TestSetRangeCheck(t *testing.T) {
	c := openFake(newFake())
	if err := c.Set("brightness", 100); err == nil {
		t.Error("out of range accepted")
	}
	if err := c.Set("auto_white_balance", 0); err != nil {
		t.Errorf("alias failed: %v", err)
	}
}

func TestPick(t *testing.T) {
	devs := []DeviceInfo{
		{Index: 0, Name: "FaceTime HD", Path: "a"},
		{Index: 1, Name: "Arducam IMX298", VendorID: 0x0c45, ProductID: 0x6366, Path: "/dev/video2"},
	}
	for sel, want := range map[string]int{"": 0, "1": 1, "0c45:6366": 1, "0C45:6366": 1, "/dev/video2": 1, "arducam": 1, "imx": 1} {
		d, err := pick(devs, sel)
		if err != nil || d.Index != want {
			t.Errorf("pick(%q) = %d, %v; want %d", sel, d.Index, err, want)
		}
	}
	for _, sel := range []string{"7", "1234:5678", "logitech"} {
		if _, err := pick(devs, sel); !errors.Is(err, ErrNoCamera) {
			t.Errorf("pick(%q) err = %v", sel, err)
		}
	}
	if _, err := pick(nil, ""); !errors.Is(err, ErrNoCamera) {
		t.Error("empty list should fail")
	}
}

func TestParseValue(t *testing.T) {
	b := Control{Name: "white_balance_auto", Type: TypeBool, Max: 1, Default: 1}
	pl := Control{Name: "power_line_frequency", Type: TypeMenu, Max: 2}
	n := Control{Name: "gain", Type: TypeInt, Min: 0, Max: 100, Default: 32}
	cases := []struct {
		c    Control
		in   string
		want int
	}{
		{b, "on", 1}, {b, "OFF", 0}, {b, "manual", 0}, {b, "auto", 1}, {b, "default", 1},
		{pl, "60hz", 2}, {pl, "50", 1}, {pl, "off", 0},
		{n, "42", 42}, {n, "max", 100}, {n, "default", 32}, {n, "-3", -3},
	}
	for _, tc := range cases {
		got, err := tc.c.ParseValue(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s ParseValue(%q) = %d, %v; want %d", tc.c.Name, tc.in, got, err, tc.want)
		}
	}
	if _, err := n.ParseValue("lots"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestIsAuto(t *testing.T) {
	c := openFake(newFake())
	for n, want := range map[string]bool{"exposure_auto": true, "white_balance_auto": true, "focus_auto": true, "exposure": false, "gain": false} {
		if c.isAuto(n) != want {
			t.Errorf("isAuto(%s) = %v", n, !want)
		}
	}
}

func TestResetWritesDefaultsUnderAuto(t *testing.T) {
	f := newFake()
	f.vals["exposure_auto"], f.vals["exposure"] = 0, 999
	c := openFake(f)
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	if f.vals["exposure"] != 157 || f.vals["exposure_auto"] != 1 || f.vals["white_balance"] != 4600 {
		t.Errorf("reset left %v", f.vals)
	}
}
