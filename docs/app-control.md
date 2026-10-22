# Xiaozhi screen-app tools on crown

This optional fork feature connects Xiaozhi MCP tools to the **local Chromium browser on Echo Show 8 1st gen (`crown`)**. Rendering, keyboard, media decoding and the MCP bridge run on the Echo. No laptop renderer, casting server or Home Assistant is required. The voice model and external websites still require Internet access.

## Tools and intended voice requests

| Tool | Input | Intended request |
| --- | --- | --- |
| `open_app` | `app`: `browser`, `youtube`, `netflix` | “Marvin, mở YouTube”; “Open Netflix” |
| `search_on_screen` | `target`: `web`, `youtube`, `netflix`; `query`: 2–500 characters | “Tìm video du lịch Đà Lạt trên YouTube”; “Mở web tìm thời tiết Đà Lạt” |
| `close_app` | none | “Đóng trình duyệt, về đồng hồ” |
| `app_status` | none | Check whether the requested app is queued, starting, open, closed or failed |

These are natural-language examples for a tool-capable Xiaozhi model, not locally parsed wake-word commands. The model must discover and invoke the appropriate MCP tool. `open` confirms the local browser window is ready, not that a site loaded, an account signed in, a video started or an answer was verified.

`search_on_screen` displays Google/YouTube/Netflix results. Existing `web_search` retrieves Exa excerpts for a spoken answer. Opening a search page does not use the Exa quota, but is not equivalent to reading/verifying its results. Anonymous Exa search remains rate limited.

## Voice/display lifecycle

A tool accepts the action immediately as `queued`; it must not wait for voice Idle while the cloud is waiting for the tool result. The display starts the browser after speech really drains, not merely when a TTSStop event paints Idle. An accepted screen handoff temporarily skips its automatic follow-up listen; ordinary conversation settings remain unchanged. New accepted commands supersede older ones. Close, explicit voice cancellation, mute and higher-priority safety screens retire pending work. A cloud reconnect does not: the server closes idle sessions about once a minute, and that transport drop must not cancel an app that is still starting. `app_status` remains a successful read (`ok: true`) whenever the last request did not fail, including right after boot.

The existing single framebuffer owner still arbitrates browser, native video, phone, PIN/setup/update and the clock. Native YouTube full-screen playback retains its separate fd3 handoff. Browser readiness is an independent fd4 acknowledgement after actual window/work-area checks; PCM startup alone cannot report `open`. `closed` must wait for owned native decoder cleanup as well as the browser process group.

## Local security boundary

The existing unprivileged MCP sidecar makes one bounded Unix request to `/run/techo5-app-control/control.sock`. The root-owned directory is group `appcontrol` GID **65532**, mode0750; socket mode0660. The daemon additionally checks peer UID0 or65534. The sidecar remains UID/GID65534 with supplementary inet3003 and appcontrol65532. Alpine's existing nogroup65533 is not reused.

Only fixed actions and providers are allowed. The model cannot supply a shell command, arbitrary URL, JavaScript, executable or general encrypted-device API key. Queries are encoded into allowlisted HTTPS provider routes and independently checked by the browser supervisor. Requests/responses are bounded to4096bytes with three-second local deadlines. Status/counters do not include query text or destination URLs. Search text necessarily reaches the chosen website and Xiaozhi's cloud/model.

No remote-debugging port is required or left open. The old Linux4.9 kernel/chroot remains an experimental browser security environment, not a modern hardened desktop. Keep account risk in mind.

## Build and install

Build matching daemon and sidecar with the toolchain in [building.md](building.md) and [xiaozhi-mcp.md](xiaozhi-mcp.md). Package **both**, plus `appcontrol:x:65532:` in the offline rootfs `/etc/group`. Keep the existing init-supervised sidecar launcher and private persistent endpoint file; do not put credentials into an image or Git.

Install updated `tools/linux/apps/session.py` and `keyboard.py` into the separately prepared Chromium runtime while the browser is closed. The launcher at `tools/linux/apps/browser-session` uses `/data/techo5-apps/netflix-trial/rootfs`, runs Chromium as the existing unprivileged browser user, and preserves fd3/fd4/fd5 across the root supervisor only. The browser and keyboard do not inherit fd4. Only the keyboard inherits fd5, the dictation socket ([local apps](local-apps.md#display-audio-and-security-boundaries)); while the cloud's latest input is dictation, `open`, `search` and `close` requests are refused. This runtime path is retained to preserve installed profiles; it is not part of the firmware rootfs.

Do not use a new daemon with an old browser supervisor that never acknowledges readiness. Do not launch Xorg separately from the native app owner. See [local-apps.md](local-apps.md) for runtime prerequisites and browser limitations.

Deploy only a verified crown ARMv7/CGO-disabled image through the existing inactive-slot `slotctl` workflow. Keep the current good slot, its verified archive, working USB console and private configuration backup before replacement. Do not use Android installers, raw partition writes, kernel changes or forced health commits for this feature.

## Verification and limitations

Verify the sidecar catalog includes all five tools (including `web_search`). Make an actual SDK `tools/call` and observe queued→starting→open, then close→closed and the clock. Exercise Vietnamese/English query encoding, unavailable runtime, superseding commands, mute/cancel during startup, and native video handoff/return. Test while speaking and with automatic follow-ups enabled.

Recorded on a physical crown with `v1.3.5_app-tools.2` (sidecar 1.2.0) after a slot install and reboot:
- `app_status` returned `ok`/`closed` right after boot.
- YouTube, web (Vietnamese queries) and Netflix opened through SDK `tools/call` and reached `open`.
- `close_app` reached `closed`, with no browser processes left.
- Three requests sent about 7 s before the cloud's idle-session drop still reached `open`.

Catalog discovery and a direct SDK call do **not** prove a spoken model invocation. That requires a real spoken request, corresponding app-tool counters, and the visible result. Do not describe it as voice-verified until that observation exists. Netflix login and content entitlement are separate from app opening; some titles/direct URLs can still return E100.

## Rollback

Close the browser. Restore the previously backed-up root-owned browser launcher/session/keyboard as a matching set, then select the recorded good firmware slot with the official `slotctl rollback` path and reboot. Check the target slot hash/state first. Retain both browser profiles and the private MCP endpoint. A firmware rollback does not automatically restore scripts stored on `/data`; explicitly restore those backups too. No Wi-Fi/account reset, boot flashing or partition formatting is needed.
