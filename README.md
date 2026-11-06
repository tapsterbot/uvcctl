# uvcctl

uvcctl provides manual control of USB camera settings and keeps them that way. It's a small command-line tool and Go library for setting exposure, white balance, gain, focus and the other image controls to exact values, saving them, and re-applying them whenever the camera resets. It runs on macOS, Linux and Windows, and works with any UVC camera: Arducam, Logitech and most other USB webcams.

```
$ uvcctl show
Camera: Arducam_16MP (0c45:0290)

CONTROL                 VALUE  RANGE          DEFAULT
exposure_auto           on     on/off         on
exposure                157    1..5000 100µs  157      (set by exposure_auto)
gain                    0      0..100         0
white_balance_auto      on     on/off         on
white_balance           4600   2800..6500 K   4600     (set by white_balance_auto)
focus_auto              on     on/off         on
focus                   144    1..1023        144      (set by focus_auto)
brightness              0      -64..64        0
contrast                32     0..64          32
...
```

---

## Auto modes and resets: why the image changes

By default, a USB camera is tuned for convenience, not repeatability. Two things work against getting the same image every time:

1. **The auto modes are on.** Cameras ship with auto exposure, auto white balance and autofocus switched on. They keep adjusting the image as the lighting or the scene changes. That's what they're for, but it means the same scene can look different from one run to the next. Setting a value such as `exposure` does nothing while its auto mode is on: depending on the OS, the camera ignores the value or refuses it. Turn the auto mode off first, then set the value. uvcctl does this for you.

2. **The camera forgets everything when it loses power.** USB cameras keep their settings in temporary memory, not in storage that survives power loss. Unplugging the camera, rebooting the computer, sleep and USB hub resets all put the camera back to factory defaults, auto modes included. Your settings have to be stored on the computer and applied again. uvcctl keeps them in a small *profile* file and can re-apply it automatically.

Opening and closing the camera doesn't normally reset the values you set, but other apps can change them, and some do when they open the camera. Values an auto mode picked are less dependable: some cameras, including the Arducam 16MP, drop the exposure and white balance their auto modes chose when the next stream starts (see [About `lock`](#about-lock)).

---

## Getting started: fixed, repeatable camera settings

Have the camera plugged in, and have your usual camera software running (the Tapster software, a camera app, or OBS) so you can see the image while you adjust it.

### 1. Install

Download the zip for your system from the releases page, unzip it, and put `uvcctl` (or `uvcctl.exe`) somewhere on your PATH, or just run it from the unzipped folder.

- **macOS:** the binary isn't signed yet, so the first time, clear the download quarantine flag:
  `xattr -d com.apple.quarantine ./uvcctl`
- **Linux:** your user needs access to `/dev/video*`. On most distros that means joining the `video` group, then logging out and back in:
  `sudo usermod -aG video $USER`
- **Windows:** no setup. Run it from PowerShell or Command Prompt.

To build from source instead, see [Building](#building).

### 2. Find your camera

```
$ uvcctl list
INDEX  NAME          USB ID     PATH
0      Arducam_16MP  0c45:0290  usb:0x02100000
```

With one camera, uvcctl uses it automatically. With several, add `-d` to any command: an index (`-d 1`), a USB ID (`-d 0c45:0290`), or part of the name (`-d arducam`).

### 3. Look at what the camera supports

```
$ uvcctl show
```

Each camera reports its own ranges, so check `RANGE` before choosing values. `(set by exposure_auto)` means that value is currently under the camera's control.

### 4. Set fixed values

Set exposure and white balance by hand. Watch the live image while you do this and adjust until it looks right:

```
$ uvcctl set exposure=167 white_balance=4600 gain=0
(turning exposure_auto off so exposure sticks)
(turning white_balance_auto off so white_balance sticks)
exposure_auto=off
exposure=167
white_balance_auto=off
white_balance=4600
gain=0
```

Some starting points:

- **Exposure** is in units of 0.1 ms on macOS and Linux, so `167` means 16.7 ms. Under mains lighting, pick a multiple of the flicker period to avoid banding and pulsing brightness:
  - 60 Hz power (North America and others): `83`, `167`, `250`, `333`
  - 50 Hz power (Europe, Asia and others): `100`, `200`, `300`

  At 30 fps, keep exposure at or below `333`, or the frame rate drops. (On Windows, exposure uses a different scale; see [Platform notes](#platform-notes).)
- **Gain** amplifies the signal, but also the noise. Keep it low (`0` if you can) and fix a dark image with a longer exposure or more light.
- **White balance** is a color temperature in kelvin: about `3000` for warm indoor light, `4000`–`4600` for typical office LEDs, and `5500`–`6500` for daylight.
- **Focus:** the Tapster's camera-to-screen distance is fixed, so lock it too: `uvcctl set focus=144`. Adjust until the screen is sharp.
- **Anti-flicker:** `uvcctl set power_line_frequency=60hz` (or `50hz`). This mostly matters when auto exposure is on, but it's worth setting either way.

Brightness, contrast, saturation, sharpness, gamma and hue don't change on their own. Change them only if you want a different look.

### 5. Save the settings

```
$ uvcctl save camera.json
Saved 15 settings to camera.json
```

This is a plain JSON file. You can edit it by hand, copy it to other machines with the same camera model, and keep it under version control.

### 6. Re-apply the settings whenever the camera may have reset

After a reboot or replug, or before a test run:

```
$ uvcctl load camera.json
```

Or leave uvcctl running, and it re-applies the settings whenever something changes them or the camera reconnects:

```
$ uvcctl load camera.json --watch
16:07:57  watching; checking every 2s (Ctrl-C to stop)
16:07:57  applied profile to Arducam_16MP (0c45:0290)
16:10:17  camera disconnected
16:10:27  camera has 5 setting(s) not matching the profile: brightness 10→0, exposure 167→157, exposure_auto off→on, ...
16:10:27  applied profile to Arducam_16MP (0c45:0290)
```

To run it automatically at login, use a launchd agent (macOS), a systemd user service (Linux), or Task Scheduler (Windows). Alternatively, call `uvcctl load` from your test setup script before every run.

### Undo everything

```
$ uvcctl reset            # factory defaults, auto modes back on
```

---

## Command reference

Run `uvcctl help <command>` for the full help on any command.

| Command | What it does |
|---|---|
| `uvcctl list` | List cameras |
| `uvcctl show` | Every control, with value, range and default |
| `uvcctl get [NAME...]` | Print `NAME=VALUE` lines (all controls if no names) |
| `uvcctl set NAME=VALUE...` | Change controls; auto modes are handled in the right order |
| `uvcctl save FILE` | Save current settings to a profile |
| `uvcctl load FILE [--watch] [--interval 2s]` | Apply a profile; `--watch` keeps it applied |
| `uvcctl lock [--settle 4s] [--save FILE]` | Let the auto modes tune once, then switch them off (see below) |
| `uvcctl reset` | Factory defaults |
| `uvcctl version` | Version and platform |

Options: `-d/--device SEL` picks a camera, and `--json` (on `list`, `show` and `get`) prints JSON for scripts. Flags can go before or after the arguments. Exit codes: `0` for success, `1` for a camera or control error, `2` for bad usage.

**Values:** a number, `on`/`off` (also `auto`/`manual`), `default`, `min` or `max`. `power_line_frequency` also accepts `off`, `50hz` and `60hz`.

### Controls

| Name | What it is | Also accepted as |
|---|---|---|
| `exposure_auto` | Auto exposure on/off | `auto_exposure` |
| `exposure` | Exposure time | `exposure_absolute`, `exposure_time_absolute` |
| `gain` | Sensor gain | |
| `white_balance_auto` | Auto white balance on/off | `auto_white_balance`, `white_balance_automatic` |
| `white_balance` | White balance temperature (K) | `white_balance_temperature` |
| `focus_auto` | Autofocus on/off | `autofocus`, `focus_automatic_continuous` |
| `focus` | Focus position | `focus_absolute` |
| `brightness`, `contrast`, `saturation`, `hue`, `sharpness`, `gamma` | Image tuning | |
| `backlight_compensation` | Backlight compensation | |
| `power_line_frequency` | Anti-flicker: `off`, `50hz`, `60hz` | `anti_flicker` |
| `zoom` | Digital zoom, where supported | `zoom_absolute` |

The aliases match the names `v4l2-ctl` uses on Linux, so existing scripts translate directly.

### About `lock`

`uvcctl lock` turns the auto modes on, waits a few seconds, then turns them off, which freezes the image wherever the auto modes left it. It's a quick fix, but it has limits:

- The camera must be **streaming** while it runs, because the auto modes only adjust while video is flowing.
- Some cameras don't report the values their auto modes chose. On the **Arducam 16MP** (`0c45:0290`), this affects exposure and white balance:
  - While auto exposure or auto white balance runs, and after it's turned off, `get` and `show` return the last value you set, not what the camera is using.
  - When the next stream starts, the camera goes back to that last value you set.

  So on this camera, locking exposure and white balance only lasts until the camera is closed, and those values can't be saved. Focus is not affected: autofocus reports its value, and the value survives a new stream, so `lock` and `save` work for focus. uvcctl warns you about any values that went unreported.

For settings that last, use `set`, then `save`, then `load` (see [Getting started](#getting-started-fixed-repeatable-camera-settings)).

---

## Platform notes

| | macOS | Linux | Windows |
|---|---|---|---|
| Talks to the camera through | IOKit (USB control requests) | V4L2 (`uvcvideo` driver) | DirectShow (`IAMVideoProcAmp` / `IAMCameraControl`) |
| Works while another app is streaming | Yes | Yes | Yes |
| Exposure units | 0.1 ms | 0.1 ms | log₂ seconds: `-5` ≈ 1/32 s, `-6` ≈ 1/64 s, `-7` ≈ 1/128 s |
| Device path shown by `list` | USB location | `/dev/videoN` | DirectShow device path |

Because Windows uses a different exposure scale, save one profile per OS. uvcctl warns you if you load a profile saved on a different OS.

Built-in laptop cameras (such as the MacBook FaceTime camera) usually aren't UVC devices, so they don't appear in the list.

---

## Using it as a Go library

```go
import "github.com/tapsterbot/uvcctl/uvc"

cam, err := uvc.Open("")          // "" = first camera; or index, "vvvv:pppp", name
if err != nil { ... }
defer cam.Close()

// Apply handles ordering: auto modes off first, then values, then auto modes on.
err = cam.Apply(map[string]int{
    "exposure_auto": 0, "exposure": 167,
    "white_balance_auto": 0, "white_balance": 4600,
    "gain": 0,
})

for _, c := range cam.Controls() {
    fmt.Println(c.Name, c.Value, c.Min, c.Max, c.Default)
}
```

See `uvc/uvc.go` for the full API: `Devices`, `Open`, `Get`, `Set`, `Apply`, `Snapshot`, `Lock` and `Reset`.

---

## Building

You need Go 1.22 or newer.

```
go build -o uvcctl .        # current platform
./build.sh                  # all release zips into dist/ (run on a Mac)
```

The macOS build uses cgo (IOKit), so it has to be built on a Mac; `build.sh` makes a universal arm64 + x86_64 binary. The Linux and Windows builds are pure Go and cross-compile from any machine. Release targets: macOS (universal), Linux amd64/arm64/armv7 (Raspberry Pi), and Windows amd64/arm64.

```
go test ./...
```

---

## Troubleshooting

**`no matching camera found`.** Check that `uvcctl list` shows the camera. On Linux, check `/dev/video*` permissions (the `video` group). Built-in laptop cameras aren't supported.

**A control is missing from `show`.** The camera doesn't support it. uvcctl only lists what the camera reports.

**`permission denied (is the matching auto mode still on?)` on Linux.** The kernel won't accept a manual value while its auto mode is on. Use `uvcctl set`, which turns the auto mode off for you, or set `exposure_auto=off` first.

**Settings reset after a reboot or replug.** That's expected, since the camera can't store them. Use `uvcctl load camera.json`, or `--watch`.

**The image still flickers or pulses under room lighting.** Set `power_line_frequency` to your mains frequency, and use an exposure that's a multiple of the flicker period (see step 4).

---

## License

MIT. See [LICENSE](LICENSE).
