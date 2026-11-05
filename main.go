// uvcctl reads, sets, saves and enforces USB camera (UVC) settings —
// exposure, white balance, gain, focus and friends — on macOS, Linux and
// Windows.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tapsterbot/uvcctl/uvc"
)

var version = "0.1.0"

const deviceHelp = `  -d, --device SEL   Camera to use. SEL is an index from 'uvcctl list', a USB
                     ID like 0c45:0290, a device path, or part of the name.
                     Default: the first camera.`

type command struct {
	name    string
	aliases []string
	args    string // usage line after the command name
	summary string
	help    string   // long help; flags are listed here too
	flags   []string // which shared flags it accepts
	minArgs int
	maxArgs int // -1 = unlimited
	run     func(out io.Writer, o options, args []string) error
}

var commands []*command

func init() {
	commands = []*command{
		{name: "list", aliases: []string{"ls"}, summary: "List connected cameras", maxArgs: 0,
			flags: []string{"json"},
			help: `Lists USB video cameras that expose UVC controls.

Options:
      --json         Print JSON instead of a table.`,
			run: func(out io.Writer, o options, _ []string) error { return cmdList(out, o) }},
		{name: "show", aliases: []string{"controls"}, summary: "Show every control: value, range, default", maxArgs: 0,
			flags: []string{"device", "json"},
			help: `Shows every control the camera supports, with its current value, valid
range and factory default. A note marks values an auto mode is driving.

Options:
` + deviceHelp + `
      --json         Print JSON.`,
			run: withCam(func(out io.Writer, o options, cam *uvc.Camera, _ []string) error { return cmdShow(out, o, cam) })},
		{name: "get", args: "[NAME...]", summary: "Print control values", maxArgs: -1,
			flags: []string{"device", "json"},
			help: `Prints NAME=VALUE for each control asked for (all controls if none).

Example:
  uvcctl get exposure_auto exposure white_balance

Options:
` + deviceHelp + `
      --json         Print a JSON object.`,
			run: withCam(func(out io.Writer, o options, cam *uvc.Camera, a []string) error { return cmdGet(out, o, cam, a) })},
		{name: "set", args: "NAME=VALUE...", summary: "Change controls", minArgs: 1, maxArgs: -1,
			flags: []string{"device"},
			help: `Changes one or more controls. Auto modes are switched in the order cameras
need: autos off first, then values, then autos on. Setting exposure,
white_balance or focus turns its auto mode off for you, so the value sticks.

VALUE can be a number, on/off (also auto/manual), default, min or max.
power_line_frequency also takes off, 50hz or 60hz.

Examples:
  uvcctl set exposure=167 white_balance=4600 gain=0
  uvcctl set power_line_frequency=60hz
  uvcctl set exposure_auto=on white_balance_auto=on

Options:
` + deviceHelp,
			run: withCam(func(out io.Writer, _ options, cam *uvc.Camera, a []string) error { return cmdSet(out, cam, a) })},
		{name: "save", args: "FILE", summary: "Save current settings to a profile", minArgs: 1, maxArgs: 1,
			flags: []string{"device"},
			help: `Writes the camera's current settings to FILE (JSON). Load it later with
'uvcctl load FILE'. Values an auto mode is driving are left out, because
they can't be restored.

Options:
` + deviceHelp,
			run: withCam(func(out io.Writer, _ options, cam *uvc.Camera, a []string) error { return saveProfile(cam, a[0], out) })},
		{name: "load", aliases: []string{"apply"}, args: "FILE", summary: "Apply a saved profile (--watch keeps it applied)", minArgs: 1, maxArgs: 1,
			flags: []string{"device", "watch", "interval"},
			help: `Applies the settings in FILE. Without -d, it picks the camera with the
USB ID stored in the profile.

With --watch it keeps running: it checks the camera every --interval,
re-applies the profile if anything changed a setting, and re-applies it
when the camera is unplugged and plugged back in (cameras forget their
settings when they lose power). Stop it with Ctrl-C.

Options:
` + deviceHelp + `
  -w, --watch        Keep the profile applied.
      --interval D   How often --watch checks (default 2s).`,
			run: func(out io.Writer, o options, a []string) error { return cmdLoad(out, o, a[0]) }},
		{name: "lock", summary: "Let the autos tune once, then freeze them (this session only)", maxArgs: 0,
			flags: []string{"device", "settle", "save"},
			help: `Turns auto exposure, auto white balance and autofocus on, waits --settle,
then turns them off so the image stops changing.

The camera must be streaming (some app has it open), or the autos have
nothing to tune. Many cameras, including the Arducam 16MP, don't report the
values their autos picked, and fall back to the last manual values when a
new stream starts. In that case the lock only lasts until the stream
closes. For settings that last, use 'set' + 'save' + 'load'.

Options:
` + deviceHelp + `
      --settle D     How long to let the autos run (default 4s).
      --save FILE    Also save a profile, if the camera reported its values.`,
			run: withCam(func(out io.Writer, o options, cam *uvc.Camera, _ []string) error { return cmdLock(out, o, cam) })},
		{name: "reset", summary: "Restore factory defaults", maxArgs: 0,
			flags: []string{"device"},
			help: `Sets every control, auto modes included, back to the camera's default.

Options:
` + deviceHelp,
			run: withCam(func(out io.Writer, _ options, cam *uvc.Camera, _ []string) error {
				if err := cam.Reset(); err != nil {
					return err
				}
				fmt.Fprintln(out, "Restored factory defaults.")
				return nil
			})},
		{name: "version", summary: "Print the version", maxArgs: 0,
			help: "Prints the uvcctl version and platform.",
			run: func(out io.Writer, _ options, _ []string) error {
				fmt.Fprintf(out, "uvcctl %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
				return nil
			}},
	}
}

func withCam(fn func(io.Writer, options, *uvc.Camera, []string) error) func(io.Writer, options, []string) error {
	return func(out io.Writer, o options, args []string) error {
		cam, err := uvc.Open(o.device)
		if err != nil {
			return err
		}
		defer cam.Close()
		return fn(out, o, cam, args)
	}
}

func findCommand(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
		for _, a := range c.aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, `uvcctl - read, set and lock USB camera (UVC) settings

Usage:
  uvcctl <command> [arguments] [options]

Commands:
`)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  %s %s\t%s\n", c.name, c.args, c.summary)
	}
	tw.Flush()
	fmt.Fprint(out, `
Run 'uvcctl help <command>' for details and options.

Fix the camera's settings in place:
  uvcctl set exposure=167 white_balance=4600 gain=0 power_line_frequency=60hz
  uvcctl save camera.json
  uvcctl load camera.json --watch
`)
}

func printHelp(out io.Writer, c *command) {
	fmt.Fprintf(out, "Usage: uvcctl %s %s\n\n%s\n\n%s\n", c.name, c.args, c.summary, c.help)
}

type options struct {
	device   string
	json     bool
	settle   time.Duration
	save     string
	watch    bool
	interval time.Duration
}

// errUsage is reported with exit code 2.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func main() {
	err := run(os.Args[1:], os.Stdout)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "uvcctl:", err)
	var u errUsage
	if errors.As(err, &u) {
		os.Exit(2)
	}
	os.Exit(1)
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}
	name, args := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		if len(args) > 0 {
			if c := findCommand(args[0]); c != nil {
				printHelp(out, c)
				return nil
			}
			return errUsage{fmt.Sprintf("unknown command %q", args[0])}
		}
		printUsage(out)
		return nil
	case "--version", "-v":
		name = "version"
	}
	c := findCommand(name)
	if c == nil {
		return errUsage{fmt.Sprintf("unknown command %q (run 'uvcctl help')", name)}
	}

	var o options
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, f := range c.flags {
		switch f {
		case "device":
			fs.StringVar(&o.device, "d", "", "")
			fs.StringVar(&o.device, "device", "", "")
		case "json":
			fs.BoolVar(&o.json, "json", false, "")
		case "settle":
			fs.DurationVar(&o.settle, "settle", 4*time.Second, "")
		case "save":
			fs.StringVar(&o.save, "save", "", "")
		case "watch":
			fs.BoolVar(&o.watch, "w", false, "")
			fs.BoolVar(&o.watch, "watch", false, "")
		case "interval":
			fs.DurationVar(&o.interval, "interval", 2*time.Second, "")
		}
	}
	// Allow flags before or after positional arguments.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				printHelp(out, c)
				return nil
			}
			return errUsage{fmt.Sprintf("%s: %v (run 'uvcctl help %s')", c.name, err, c.name)}
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos, args = append(pos, args[0]), args[1:]
	}
	if len(pos) < c.minArgs || (c.maxArgs >= 0 && len(pos) > c.maxArgs) {
		return errUsage{fmt.Sprintf("usage: uvcctl %s %s", c.name, c.args)}
	}
	if o.interval <= 0 && o.watch {
		return errUsage{"--interval must be positive"}
	}
	return c.run(out, o, pos)
}

func describe(d uvc.DeviceInfo) string {
	if id := d.ID(); id != "" {
		return fmt.Sprintf("%s (%s)", d.Name, id)
	}
	return d.Name
}

func cmdList(out io.Writer, o options) error {
	devs, err := uvc.Devices()
	if err != nil {
		return err
	}
	if o.json {
		return writeJSON(out, devs)
	}
	if len(devs) == 0 {
		fmt.Fprintln(out, "No cameras found.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "INDEX\tNAME\tUSB ID\tPATH")
	for _, d := range devs {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", d.Index, d.Name, d.ID(), d.Path)
	}
	return tw.Flush()
}

func cmdShow(out io.Writer, o options, cam *uvc.Camera) error {
	ctrls := cam.Controls()
	if o.json {
		return writeJSON(out, map[string]any{"camera": cam.Info(), "controls": ctrls})
	}
	fmt.Fprintf(out, "Camera: %s\n\n", describe(cam.Info()))
	cur := map[string]int{}
	for _, c := range ctrls {
		cur[c.Name] = c.Value
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CONTROL\tVALUE\tRANGE\tDEFAULT\t")
	for _, c := range ctrls {
		rng := fmt.Sprintf("%d..%d", c.Min, c.Max)
		if c.Type == uvc.TypeBool {
			rng = "on/off"
		}
		if c.Unit != "" {
			rng += " " + c.Unit
		}
		note := ""
		if c.Auto != "" && cur[c.Auto] != 0 {
			note = "(set by " + c.Auto + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.FormatValue(c.Value), rng, c.FormatValue(c.Default), note)
	}
	return tw.Flush()
}

func cmdGet(out io.Writer, o options, cam *uvc.Camera, names []string) error {
	if len(names) == 0 {
		for _, c := range cam.Controls() {
			names = append(names, c.Name)
		}
	}
	vals := map[string]int{}
	var errs []error
	for _, n := range names {
		c, ok := cam.Control(n)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: %w", uvc.CanonicalName(n), uvc.ErrUnsupported))
			continue
		}
		vals[c.Name] = c.Value
		if !o.json {
			fmt.Fprintf(out, "%s=%s\n", c.Name, c.FormatValue(c.Value))
		}
	}
	if o.json {
		if err := writeJSON(out, vals); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

func parseAssignments(cam *uvc.Camera, pairs []string) (map[string]int, error) {
	vals := map[string]int{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("expected NAME=VALUE, got %q", p)
		}
		c, ok := cam.Control(k)
		if !ok {
			return nil, fmt.Errorf("%s: %w (run 'uvcctl show' to see what this camera has)", uvc.CanonicalName(k), uvc.ErrUnsupported)
		}
		n, err := c.ParseValue(v)
		if err != nil {
			return nil, err
		}
		vals[c.Name] = n
	}
	return vals, nil
}

func cmdSet(out io.Writer, cam *uvc.Camera, pairs []string) error {
	if len(pairs) == 0 {
		return errors.New("usage: uvcctl set NAME=VALUE...")
	}
	vals, err := parseAssignments(cam, pairs)
	if err != nil {
		return err
	}
	// Setting a value that an auto mode drives implies turning that auto off.
	for name := range vals {
		if c, ok := cam.Control(name); ok && c.Auto != "" {
			if _, explicit := vals[c.Auto]; !explicit {
				vals[c.Auto] = 0
				fmt.Fprintf(out, "(turning %s off so %s sticks)\n", c.Auto, name)
			}
		}
	}
	err = cam.Apply(vals)
	for _, c := range cam.Controls() {
		if _, ok := vals[c.Name]; ok {
			fmt.Fprintf(out, "%s=%s\n", c.Name, c.FormatValue(c.Value))
		}
	}
	return err
}

func cmdLock(out io.Writer, o options, cam *uvc.Camera) error {
	fmt.Fprintf(out, "Letting %s auto-tune for %v (the camera must be streaming)...\n", describe(cam.Info()), o.settle)
	unreported, err := cam.Lock(o.settle)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Locked: auto exposure, auto white balance and autofocus are now off.")
	if len(unreported) == 0 {
		for _, c := range cam.Controls() {
			if c.Auto != "" || c.Name == "gain" {
				fmt.Fprintf(out, "  %s=%s\n", c.Name, c.FormatValue(c.Value))
			}
		}
		if o.save != "" {
			return saveProfile(cam, o.save, out)
		}
		return nil
	}
	fmt.Fprintf(out, `
Note: the camera didn't report new values for %s while auto was on.
Either nothing needed changing, or (common on Arducam) the camera doesn't
report what its autos picked. The image is locked for now, but on cameras
like that the lock ends when the stream closes, and the numbers can't be
saved. For settings that last, pick values by hand and save them:
  uvcctl set exposure=167 white_balance=4600
  uvcctl save camera.json
`, strings.Join(unreported, ", "))
	if o.save != "" {
		fmt.Fprintln(out, "Not saving: the profile would hold stale numbers.")
	}
	return nil
}

// Profile is the on-disk settings file.
type Profile struct {
	Camera   string         `json:"camera,omitempty"`
	Device   string         `json:"device,omitempty"`
	OS       string         `json:"os,omitempty"`
	Controls map[string]any `json:"controls"`
}

func saveProfile(cam *uvc.Camera, path string, out io.Writer) error {
	p := Profile{
		Camera:   cam.Info().Name,
		Device:   cam.Info().ID(),
		OS:       runtime.GOOS,
		Controls: map[string]any{},
	}
	snap := cam.Snapshot()
	for _, c := range cam.Controls() {
		v, ok := snap[c.Name]
		if !ok {
			continue
		}
		if c.Type == uvc.TypeInt {
			p.Controls[c.Name] = v
		} else {
			p.Controls[c.Name] = c.FormatValue(v)
		}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved %d settings to %s\n", len(p.Controls), path)
	return nil
}

func readProfile(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(p.Controls) == 0 {
		return nil, fmt.Errorf("%s: no controls in profile", path)
	}
	return &p, nil
}

func profileValues(cam *uvc.Camera, p *Profile) (map[string]int, error) {
	pairs := make([]string, 0, len(p.Controls))
	for k, v := range p.Controls {
		pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
	}
	return parseAssignments(cam, pairs)
}

func cmdLoad(out io.Writer, o options, path string) error {
	p, err := readProfile(path)
	if err != nil {
		return err
	}
	if p.OS != "" && p.OS != runtime.GOOS {
		fmt.Fprintf(out, "warning: profile was saved on %s; exposure units differ between operating systems\n", p.OS)
	}
	sel := o.device
	if sel == "" {
		sel = p.Device
	}
	if !o.watch {
		cam, err := uvc.Open(sel)
		if err != nil {
			return err
		}
		defer cam.Close()
		vals, err := profileValues(cam, p)
		if err != nil {
			return err
		}
		err = cam.Apply(vals)
		fmt.Fprintf(out, "Applied %s to %s\n", path, describe(cam.Info()))
		reportMismatch(out, cam, vals)
		return err
	}
	return watch(out, sel, p, o.interval)
}

// reportMismatch prints controls the camera didn't take as requested, and
// returns what the camera actually holds for them.
func reportMismatch(out io.Writer, cam *uvc.Camera, want map[string]int) map[string]int {
	got := map[string]int{}
	snap := cam.Snapshot()
	for name, v := range want {
		cur, ok := snap[name]
		if !ok {
			continue // driven by an auto mode
		}
		got[name] = cur
		if cur != v {
			c, _ := cam.Control(name)
			fmt.Fprintf(out, "  note: %s is %s, not %s (camera adjusted or refused it)\n", name, c.FormatValue(cur), c.FormatValue(v))
		}
	}
	return got
}

// watch keeps a profile applied: it re-applies when a value changes, and
// when the camera is unplugged and comes back.
func watch(out io.Writer, sel string, p *Profile, every time.Duration) error {
	logf := func(format string, a ...any) {
		fmt.Fprintf(out, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	logf("watching; checking every %v (Ctrl-C to stop)", every)
	var cam *uvc.Camera
	var vals, expect map[string]int
	waiting := false
	for ; ; time.Sleep(every) {
		if cam == nil {
			c, err := uvc.Open(sel)
			if err != nil {
				if !waiting {
					logf("waiting for camera: %v", err)
					waiting = true
				}
				continue
			}
			waiting = false
			v, err := profileValues(c, p)
			if err != nil {
				c.Close()
				return err
			}
			vals = v
			if d := differences(c, vals); len(d) > 0 {
				logf("camera has %d setting(s) not matching the profile: %s", len(d), strings.Join(d, ", "))
			}
			if err := c.Apply(vals); err != nil {
				logf("apply: %v", err)
			}
			logf("applied profile to %s", describe(c.Info()))
			expect = reportMismatch(out, c, vals)
			cam = c
			continue
		}
		lost := false
		for name := range expect {
			if _, err := cam.Get(name); err != nil {
				lost = true
				break
			}
		}
		drifted := differences(cam, expect)
		if lost {
			logf("camera disconnected")
			cam.Close()
			cam = nil
			continue
		}
		if len(drifted) > 0 {
			logf("settings changed (%s); re-applying", strings.Join(drifted, ", "))
			if err := cam.Apply(vals); err != nil {
				logf("apply: %v", err)
			}
			expect = reportMismatch(out, cam, vals)
		}
	}
}

// differences lists controls whose current value isn't the wanted one, as
// "name want→current", sorted.
func differences(cam *uvc.Camera, want map[string]int) []string {
	var out []string
	for name, v := range want {
		c, ok := cam.Control(name)
		if !ok {
			continue
		}
		if cur, err := cam.Get(name); err == nil && cur != v {
			out = append(out, fmt.Sprintf("%s %s→%s", name, c.FormatValue(v), c.FormatValue(cur)))
		}
	}
	sort.Strings(out)
	return out
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
