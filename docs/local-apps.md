# Local browser and YouTube on Echo Show 8

Optional, experimental support for **Echo Show 8 1st gen (`crown`) running TECHO5 Alpine ARMv7**. This is not an Android application installer. Firefox, the keyboard, YouTube extraction and video decoding all run on the Echo; no laptop renderer, casting server or Home Assistant is required. A laptop is used only to build/install and inspect diagnostics.

## Using it

With the separate application runtime installed, open **Settings → Connections → Web browser / YouTube**. These controls are hidden when the trusted launcher is absent. The clock remains the normal startup screen; the browser does not start at boot.

- Touch a web field, compose text in the keyboard, then **Dán** or **Tìm / Enter**. **URL** focuses the address bar.
- **VI** switches Vietnamese/English. Vietnamese has explicit `ă â đ ê ô ơ ư` and tone keys, not Telex. Apply the tone immediately after its target vowel; composition is normalized to NFC. Backspace handles combining marks and common emoji clusters.
- **Ẩn phím / Bàn phím** collapses/expands the keyboard while retaining navigation and video controls. Touch targets are at least 48 pixels.
- On a public YouTube watch page, press **Phát video**. The Echo extracts a compatible stream and opens its local software video player. **Dừng video** cancels extraction or playback and returns to Firefox.
- **Đóng** exits the browser and returns to the native screen. Screen-off and higher-priority native activities also close the application session rather than sharing the framebuffer concurrently.

## What has actually been exercised

On `crown`, Firefox ESR rendered Google and a real Vietnamese `đà lạt` search entered through the GTK keyboard. A public YouTube clip (`jNQXAC9IVRw`) played through local mpv: two timed framebuffer captures showed different video frames, and the private synthetic audio monitor contained nonzero PCM. This is not a claim that every YouTube video or website works, nor a physical listening test of speaker volume.

The browser's own YouTube media utility crashed on the device's old kernel/runtime. **The tested playback path is the separate Phát video button**, not Firefox's embedded Play button. The decoder selects H.264 at no more than 480p plus AAC/Opus; the verified sample was a low-resolution clip. DRM, paid/account-restricted videos and universal 720p/1080p playback are not supported claims. Internet access and a currently working YouTube extractor remain necessary.

## Runtime layout and preparation

The firmware supplies the native screen handoff, not a bundled browser rootfs. Source configuration is in `tools/linux/apps/`:

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

## Display, audio and security boundaries

Native rendering pauses while the local session owns the display. After its managed process group has exited, all framebuffer pages are invalidated and repainted. This fixes stale black side edges when returning from Xorg to the clock, without changing the clock design or theme.

Firefox, GTK, PulseAudio, yt-dlp and mpv run as UID 1000. Xorg alone needs root for the old framebuffer/input devices. It remains in the managed process group (`-keeptty`), has per-session Xauthority, and does not listen on TCP. Profile writes happen after dropping privileges. The session supervisor reaps descendants on exit; a missing keyboard is a fatal session failure.

The private app root has **no physical ALSA/microphone device nodes**. PulseAudio renders to a private null sink; only that synthetic monitor goes through a pipe to TECHO5's existing media mixer/AEC and speaker path. Firefox does not seize the physical speaker. No microphone recording is added.

The Firefox content sandbox remains enabled; no sandbox-disable flags or preferences are used. Nevertheless, Linux 4.9 and the absence of modern namespaces are a real security limitation. Firefox displays its OS-protection warning. This experimental setup is not a recommendation for banking, valuable account sessions or untrusted sensitive browsing. A chroot is not a complete security sandbox.

Logs remain root-private at `/data/techo5-apps/logs/session.log`. The local player suppresses signed media URLs and does not persist downloaded videos or account cookies. Firefox retains its ordinary local profile/history.

## Update and rollback

Keep a verified good native firmware slot and working USB console before updating. Build ARMv7/CGO-disabled `echod` with the repository toolchain and package it through the existing inactive-slot `slotctl` procedure; do not replace the bootloader/kernel, write raw partitions, or use the Android installer. Verify the payload hashes and preserved good slot before switching. Let the normal boot-health controller mark the trial good.

For an application-only rollback, close the browser first and rename the root-owned launcher to `browser-session.disabled`; after native restart its Settings controls disappear. Keep `/data/techo5-apps/runtime/home/browser` if you want to retain browsing data. To revert native code, switch to the previously verified good slot through `slotctl` and reboot. Never remove an in-use runtime or unmount its framebuffer underneath Xorg. No Wi-Fi/Xiaozhi configuration reset is needed.
