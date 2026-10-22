# Local browser and YouTube on Echo Show 8

Optional, experimental support for **Echo Show 8 1st gen (`crown`) running TECHO5 Alpine ARMv7**. This is not an Android application installer. Firefox, the keyboard, YouTube extraction and video decoding all run on the Echo; no laptop renderer, casting server or Home Assistant is required. A laptop is used only to build/install and inspect diagnostics.

## Using it

With the separate application runtime installed, open **Settings → Connections → Web browser / YouTube**. These controls are hidden when the trusted launcher is absent. The clock remains the normal startup screen; the browser does not start at boot.

- Touch a web field, compose text in the keyboard, then **Dán** or **Tìm / Enter**. **URL** focuses the address bar.
- **VI** switches Vietnamese/English. Vietnamese has explicit `ă â đ ê ô ơ ư` and tone keys, not Telex. Apply the tone immediately after its target vowel; composition is normalized to NFC. Backspace handles combining marks and common emoji clusters.
- **Ẩn phím / Bàn phím** collapses/expands the keyboard while retaining navigation and video controls. Touch targets are at least 48 pixels.
- **Cuộn lên / Cuộn xuống** scroll the page without needing a mouse wheel. Firefox is started with XInput2 touch support; use these explicit controls if one-finger dragging selects text instead of scrolling on this platform.
- On a public YouTube watch page, press **Phát video** on the bottom toolbar, not Play inside the website. **Hủy mở video** cancels extraction. After extraction the browser closes and TECHO5's existing native full-screen video player takes over. Tap the picture for its controls; stopping the native video returns to the browser unless a higher-priority screen has taken over.
- **Đóng** exits the browser and returns to the native screen. Screen-off and higher-priority native activities also close the application session rather than sharing the framebuffer concurrently.

## What has actually been exercised

On `crown`, Firefox ESR rendered Google and a real Vietnamese `đà lạt` search entered through the GTK keyboard. The page-scroll control moved a real Firefox page by 480 pixels. A public 854×480/25 fps YouTube clip (`0aMwGDsoOfc`) was severely presentation-limited through the initial mpv/Xorg prototype: 153 output frames dropped in ten seconds while decoding dropped none and the network cache stayed full. The native player subsequently logged 250 and 249 presented frames in consecutive ten-second intervals, with zero dropped frames; the device owner also reported that full-screen playback ran well. These are observations for that scenario, not a claim that every video/website works.

The browser's own YouTube media utility crashed in the tested environment. **The supported playback path is the separate Phát video button followed by native full-screen playback**; mpv/Xorg video rendering has been removed from the application path. The extractor selects H.264 up to the height on the keyboard's quality button (360p, 480p or 720p; 720p by default, remembered in the browser profile) plus AAC/Opus. At one height it prefers a stream of 30 fps or less, because the native player shows at most 30 frames a second. For 50/60 fps streams the daemon skips non-reference frames and deblocking that the panel never shows.

Measured on `crown` at 1280×800:
- YouTube 720p60 played at a steady 30 fps in real time, with 0–1 dropped frames per 10 s. Before this optimization it ran slower than real time.
- 720p24 also played at full rate.

1080p is not offered. Even with these shortcuts, 1080p24 decoded at only about 1.7× real time, below the margin 720p needs. It would also be scaled down to the 800-line panel anyway.

DRM and paid or account-restricted videos are not supported. Internet access and a currently working YouTube extractor remain necessary.

## Runtime layout and preparation

The firmware supplies the native screen handoff, not a bundled browser rootfs. Source configuration is in `tools/linux/apps/`:

Native playback also requires the existing **Video** permission switch and the bundled `/usr/local/bin/techo5-ffmpeg`. Enable Video once through Web Setup's Video section or the authenticated native API; DLNA can remain off. The local application does not bypass that permission or enable incoming DLNA requests.

| Source | Installed destination |
| --- | --- |
| `browser-session` | `/data/techo5-apps/bin/browser-session` (root, 0755) |
| `session.py`, `keyboard.py`, `player.py`, `pulse.pa` | `/data/techo5-apps/runtime/usr/local/share/techo5-apps/` (root-owned, not browser-writable) |
| `xorg.conf` | `/data/techo5-apps/runtime/etc/X11/echo.conf` |

Use the device's **signed Alpine repositories and APK keys**, matching the host release/ARMv7 architecture. Do not copy Windows executables, Android libraries or an unrelated distribution's binaries into the runtime. The working installation uses a separate APK database under `/data/techo5-apps/runtime`; nothing installs over the firmware's `/usr`.

From the root USB console, with enough free `/data` space (the tested runtime occupies about 600 MiB), create root-owned, non-symlink ancestors and install:

```sh
R=/data/techo5-apps/runtime
mkdir -p /data/techo5-apps/bin /data/techo5-apps/logs "$R"
chmod 755 /data/techo5-apps /data/techo5-apps/bin "$R"
apk --root "$R" --initdb --keys-dir /etc/apk/keys \
  --repositories-file /etc/apk/repositories --no-cache \
  --no-scripts --no-commit-hooks add \
  alpine-baselayout busybox ca-certificates firefox-esr \
  mesa-dri-gallium openbox pulseaudio pulseaudio-utils \
  python3 py3-cairo py3-gobject3 xauth xdotool \
  xf86-input-libinput xf86-video-fbdev xorg-server xprop xwd xwininfo \
  yt-dlp-core yt-dlp-ejs yt-dlp-ejs-rt-quickjs
```

After signature-verified package installation, run the generated-cache commands inside the isolated root: `update-ca-certificates`, `gtk-query-immodules-3.0 --update-cache`, and `glib-compile-schemas /usr/share/glib-2.0/schemas`. Ensure `/etc/ssl/cert.pem` inside the runtime points to `certs/ca-certificates.crt`. Install Inter regular/bold fonts under the runtime's `/usr/share/fonts/` and run `fc-cache -f` there. Do not disable certificate verification.

Create the runtime-only `browser` account with UID/GID **1000**, home `/home/browser`, and supplementary `inet` GID **3003**. The supervisor verifies ownership and prepares its private home/runtime directories. Copy the files above without changing browser profile data. Install the executable launcher **last**, after validating source hashes and dependencies. Do not start a second native daemon merely to try the browser: launch through the Settings controls so the native framebuffer/audio lease is held.

The launcher supplies transient private `/dev`, `/proc` and `/sys` mounts; it does not bind the host's `/data` into the application root. Its framebuffer/input node numbers and rotation are **crown-specific**. Reboot removes these mounts; opening the browser recreates them.

On crown, `xorg.conf` uses **Rotate CW**, paired with libinput calibration
`0 1 0 -1 0 1 0 0 1`. CCW produced a real upside-down panel even though X11
screenshots looked upright. Verify orientation against the native clock using
the physical framebuffer, not XWD alone; verify physical touch targets afterward.
Do not reverse the touch matrix merely because the display rotation changes:
the display framebuffer and touch controller have different native orientations.

## Display, audio and security boundaries

Native rendering pauses while the local session owns the display. After its managed process group has exited, all framebuffer pages are invalidated and repainted. This fixes stale black side edges when returning from Xorg to the clock, without changing the clock design or theme.

Firefox, GTK, PulseAudio and yt-dlp run as UID 1000. Xorg needs root for the old framebuffer/input devices. It remains in the managed process group (`-keeptty`), has per-session Xauthority, and does not listen on TCP. Profile writes happen after dropping privileges. The session supervisor reaps descendants on exit; a missing keyboard is a fatal session failure. The native FFmpeg decoder uses the existing separately restricted video process and network guard.

The private app root has **no physical ALSA/microphone device nodes**. PulseAudio renders to a private null sink; only that synthetic monitor goes through a pipe to TECHO5's existing media mixer/AEC and speaker path. Firefox does not seize the physical speaker. No microphone recording is added.

YouTube video is not rendered concurrently with Xorg. The extractor sends one newline-terminated JSON request on inherited **fd3**, bounded to 64 KiB including the newline. Only the supervisor, keyboard and extractor inherit that pipe, never Firefox. The daemon accepts at most one frame per browser lease, validates both media addresses, reaps the Xorg process group, invalidates framebuffer shadows, and only then starts native playback. Separate audio uses the same URL/network/TLS and pixel-allocation protections as video; advertised audio that produces no PCM fails instead of silently playing a picture. Native playback retains the original speaker/mixer/AEC path and native controls.

The Firefox content sandbox remains enabled; no sandbox-disable flags or preferences are used. Nevertheless, Linux 4.9 and the absence of modern namespaces are a real security limitation. Firefox displays its OS-protection warning. This experimental setup is not a recommendation for banking, valuable account sessions or untrusted sensitive browsing. A chroot is not a complete security sandbox.

Logs remain root-private at `/data/techo5-apps/logs/session.log`. Video diagnostics keep only URL origins: signed paths, query strings, userinfo and fragments are omitted. The extractor does not persist downloaded videos or account cookies. Firefox retains its ordinary local profile/history.

## Update and rollback

Keep a verified good native firmware slot and working USB console before updating. Build ARMv7/CGO-disabled `echod` with the repository toolchain and package it through the existing inactive-slot `slotctl` procedure; do not replace the bootloader/kernel, write raw partitions, or use the Android installer. Verify the payload hashes and preserved good slot before switching. Let the normal boot-health controller mark the trial good.

For an application-only rollback, close the browser first and rename the root-owned launcher to `browser-session.disabled`; after native restart its Settings controls disappear. Keep `/data/techo5-apps/runtime/home/browser` if you want to retain browsing data. To revert native code, switch to the previously verified good slot through `slotctl` and reboot. Never remove an in-use runtime or unmount its framebuffer underneath Xorg. No Wi-Fi/Xiaozhi configuration reset is needed.
