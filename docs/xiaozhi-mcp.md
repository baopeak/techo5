# Optional on-device Xiaozhi MCP search and app control

This fork supplies an optional MCP sidecar in [`xiaozhi-search/`](../xiaozhi-search/).
It is a separate Go module and process, **not part of `echod`**. The device connects directly to
Xiaozhi's cloud MCP endpoint and Exa's official keyless MCP service; no laptop bridge is needed.
The same cloud connection exposes four bounded on-device display tools through `echod`.
The ordinary [base rootfs build](building.md#4-the-root-filesystem) does not add it automatically.

## Scope and privacy

The custom, unsigned [fork prereleases](https://github.com/baopeak/techo5/releases), including
`v1.3.5-xiaozhi-mcp.1`, target **Echo Show 8 1st gen (2019, `crown`) already running TECHO5 Alpine
Linux with working A/B slots**. They are not upstream signed OTA releases. This prerelease provides
binaries and the launcher, **not a ready-made rootfs or boot image**. Package a custom rootfs below;
do not use these artifacts on Android, `athena`, or another Echo variant. No boot-partition change
is needed.

Search uses `https://mcp.exa.ai/mcp?tools=web_search_exa`. Anonymous use is free but rate limited;
the exact quota is unknown and no guaranteed published allowance is claimed. No Exa API key or
paid fallback is configured. Xiaozhi's separate official paid search extension can remain off.
Unavailable or rate-limited search returns an error, not invented results.
The shared anonymous quota has been observed exhausted during a live search. Free search is not
promised to be usable at any particular time.

Queries leave the device for Exa; tool results and excerpts pass through Xiaozhi's cloud and its
chosen model. These third parties have their own data-handling terms. Read-only search does not
make excerpts trustworthy instructions: ask the model to cite URLs and check source dates.
The endpoint URL contains an account credential. Supply your own through the cloud's MCP setup;
never publish it, put it in command history, or include it in screenshots, logs, rootfs archives or
release assets. The current client accepts only the official `api.xiaozhi.me` WSS MCP endpoint;
see [`readEndpoint`](../xiaozhi-search/main.go), not the voice backend's host setting.

## On-device screen tools (sidecar 1.2.0)

All five tools use the **existing** private Xiaozhi WSS MCP connection. There is no second
cloud endpoint, TCP listener, arbitrary URL argument, or shell-command argument.

| Tool | Arguments | Meaning |
| --- | --- | --- |
| `web_search` | `query`, optional `max_results` (1–5) | Existing read-only Exa search, returning source excerpts and URLs. |
| `open_app` | `app`: `browser`, `youtube`, or `netflix` | Request the named app on the Echo screen. |
| `search_on_screen` | `target`: `web`, `youtube`, or `netflix`; `query` | Navigate to provider search on the Echo screen, not return search evidence. |
| `close_app` | none | Cancel pending app work and request asynchronous closure. |
| `app_status` | none | Read the daemon's actual display-app state. |

Every input schema rejects extra properties. Display queries must contain 2–500 Unicode code
points after trimming. Provider URLs are built by the daemon, never supplied by the cloud.
Opening, navigating, and closing are state-mutating tools; only `app_status` is read-only.
Screen searches send queries to the selected provider; tool arguments/results also pass through
Xiaozhi's cloud and model. Neither queries nor URLs are included in app-control results/status files.

Successful screen-tool results contain `ok`, `state`, and optional `app`/`request_id`.
`queued` means accepted for later execution; `starting` means startup is in progress.
**Neither means the app is visible.** Only `open`, following browser readiness acknowledgement,
confirms a ready browser window; it does not prove account login, Netflix playback, or a
successful provider search. `closed` confirms completion; a queued close is not yet closed.
Check `app_status` rather than claiming success prematurely. No automatic retry is performed:
a connection failure after sending may leave the action queued, so inspect status before retrying.
Unavailable control, daemon rejection, malformed/oversized responses, and a three-second bridge
deadline fail the tool rather than returning fallback text as success.

The local bridge is only `/run/techo5-app-control/control.sock`, owned by root and dedicated
group `appcontrol` (`65532`), mode `0660`, in a root-owned `0750` directory with that group.
The packaged rootfs must reserve `appcontrol:x:65532:` in `/etc/group` (do not reuse Alpine's
`nogroup` GID `65533`). `echod` validates peer UID `65534` (or root for diagnostics).
One newline-delimited JSON request/response is limited to 4096 bytes each; unknown fields,
invalid enums, invalid UTF-8, and trailing JSON are rejected. A compatible daemon and browser
launcher are required; an older search-only image reports app control unavailable.

Voice-busy requests are queued without waiting for the current cloud tool call to finish.
The daemon applies latest-wins app intent after voice becomes idle, without bypassing PIN/setup,
phone, update, or off-screen safety priorities. Explicit close/mute/cancellation and safety
interruptions must not cause an old queued request to reopen later. The sidecar does not launch
browser processes itself or replace the existing native media/voice path.


## Build and package

Use Go **1.26 or later**. From the repository root in a POSIX shell (Linux or WSL):

```sh
cd xiaozhi-search
go test ./...
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o ../bin/xiaozhi-search-mcp .
cd ..
```

The result is a static ARMv7 sidecar. Build/test ownership is
[`go.mod`](../xiaozhi-search/go.mod), [`main_test.go`](../xiaozhi-search/main_test.go),
[`app_control_test.go`](../xiaozhi-search/app_control_test.go),
[`main.go`](../xiaozhi-search/main.go), and [`app_control.go`](../xiaozhi-search/app_control.go).
Version `1.2.0` adds app-control tools; earlier search/reboot observations do not validate this binary.
A generic local build is not promised to match a prerelease checksum: release build IDs and inputs
also matter. Verify downloaded artifacts against the release's checksum before packaging them.

To assemble your own image, first build a **crown-compatible** base rootfs using
[building.md](building.md), saved as `build/rootfs.tar.gz`. On Linux/WSL, use a new, empty staging
directory (the `mkdir` below deliberately fails if it already exists). Add the sidecar to the
**offline image only**:

```sh
ROOTFS="$PWD/build/rootfs-mcp-stage"
sudo mkdir "$ROOTFS"
sudo tar --numeric-owner -xzf build/rootfs.tar.gz -C "$ROOTFS"
sudo install -m 0755 bin/xiaozhi-search-mcp "$ROOTFS/usr/local/sbin/xiaozhi-search-mcp"
sudo install -m 0755 xiaozhi-search/xiaozhi-search-run "$ROOTFS/usr/local/sbin/xiaozhi-search-run"
```

Append this line once to the staged image's `etc/inittab`, retaining the existing TECHO5 entries:

```text
::respawn:/usr/local/sbin/xiaozhi-search-run
```

The image must provide BusyBox init, `nice`, and `setpriv --no-new-privs`; the tested base image
provides `setpriv` as a BusyBox applet, not an additional package.
The [launcher](../xiaozhi-search/xiaozhi-search-run) is the executable owner for startup and resource
settings. It waits for the private enable marker, creates RAM-only status storage, and starts the
service through init's respawn supervision. The [Linux privilege setup](../xiaozhi-search/priv_linux.go)
drops to UID/GID `65534`, retaining supplementary group `inet` (`3003`), which `crown` needs
for networking, and app-control group `65532`. Do not run it as root, discard `3003`, or broaden
the bridge permissions. The existing launcher invokes this privilege setup; no extra listener is needed.

Repack the staged rootfs as a gzip tar archive with numeric ownership preserved and files rooted
at `./`, as in the base image. Include no private `/data` contents or endpoint file. Keep the
base image's board configuration, TECHO5 boot files, and vendor handling unchanged. Do not copy
a binary over the running daemon, bind-mount a replacement, or remount the active root to add
this service; deploy the complete image through the inactive slot below.

```sh
sudo tar --numeric-owner -czf build/rootfs-mcp.tar.gz -C "$ROOTFS" .
```

## Install through the inactive slot

Use an authenticated root shell on your own device. Enable SSH using the existing
[TECHO5 setup](setup.md), and transfer the verified rootfs archive to a private staging location
such as `/data/local/tmp/rootfs-mcp.tar.gz` (create the directory if needed). For SCP, use `scp -O`
because these devices do not provide SFTP. Never transfer your endpoint with the release archive.

1. Run `slotctl status`. Identify the **booted** and **active** slots and their states. Continue
   only when the booted slot is also active and **good**, and you are willing to replace the other
   slot. Record the current good slot as your rollback target. If a trial is unsettled or active
   differs from booted, resolve it first; do not guess which slot installation will replace.
2. Use the official [`slotctl`](../tools/linux/slotctl):

   ```sh
   slotctl install /data/local/tmp/rootfs-mcp.tar.gz
   slotctl status
   sync
   reboot
   ```

   Installation replaces the inactive slot and selects it for a trial. Do not install another
   image while testing: doing so could overwrite your original known-good rollback slot.
3. After reboot, reconnect and run `slotctl status`. The existing TECHO5 boot logic commits a
   settled trial automatically. Confirm the expected slot/version and a `good` state; do not
   force `slotctl commit` merely to silence a failing trial. Test display, wake/listening, audio
   and networking as well as MCP before treating the new image as usable.

Slot behavior and rescue procedures are owned by
[tools/linux/README.md](../tools/linux/README.md). No manual slot-directory edits, filesystem
remounts, partition formatting or boot flashing are part of this procedure.

## Supply a private endpoint and enable

On the device, create the private persistent directory. Open the file in an editor rather than
passing its contents on a shell command line:

```sh
umask 077
mkdir -p /data/misc/xiaozhi-search
chmod 0700 /data/misc/xiaozhi-search
touch /data/misc/xiaozhi-search/endpoint.txt
chmod 0600 /data/misc/xiaozhi-search/endpoint.txt
vi /data/misc/xiaozhi-search/endpoint.txt
```

Paste **your own complete cloud MCP WSS endpoint** as a single line. No credential is provided by
this repository. Then apply ownership and enable startup:

```sh
chown 65534:65534 /data/misc/xiaozhi-search
chown 65534:65534 /data/misc/xiaozhi-search/endpoint.txt
touch /data/misc/xiaozhi-search/enabled
chmod 0600 /data/misc/xiaozhi-search/enabled
```

The init launcher checks for the marker and readable endpoint every 30 seconds while disabled;
no separate laptop process or rootfs edit is required. The credential stays on persistent `/data`,
not in either released image. To replace it, stop the service below, edit the private file, retain
its owner/mode, then recreate `enabled`.

## Observe and test

Read `/run/xiaozhi-search/status.json` on the device. It contains connection, initialization,
tool-catalog and call counters, not endpoint tokens, queries or results. `tool_calls` counts
executed handlers; `successful_searches` remains specific to Exa `web_search`.
`open_app_calls`, `search_on_screen_calls`, `close_app_calls`, and `app_status_calls` are distinct
app-tool counters. `app_tool_errors` records bridge/handler failures, while `app_queued_responses`
counts only queued acknowledgements, **not apps opened**. Schema-rejected calls may not reach handlers.
Status is RAM-only and resets on reboot. The launcher discards process stdout/stderr;
do not add credential-bearing debug logs to a public report.

For a direct provider check, the binary accepts `-search` with a non-sensitive public query.
This contacts Exa and prints excerpts; it does **not** prove cloud model tool calling. In Xiaozhi,
choose a model supporting tools, confirm `web_search` is discovered, and make a real spoken
request to search for a public topic. Check that call/success counters advance and that the spoken
answer cites returned sources. Follow [voice setup](xiaozhi.md) for microphone/backend settings.

A previous custom image with sidecar `1.1.0` survived a real reboot with a good rollback slot.
Vietnamese and English sample searches returned primary-source results then, and cloud MCP
initialization/catalog discovery was observed. A later live search exhausted the shared free quota;
`1.1.1` corrects recognition of provider limit/error text, but is **not device-reboot validated**.
**A voice-triggered tool call has not yet been observed.** Do not treat catalog discovery, a direct
search, or this guide as proof that your cloud model will invoke it.

## Stop or roll back

To disable the sidecar persistently and close the current connection without changing the rootfs:

```sh
rm -f /data/misc/xiaozhi-search/enabled
sync
reboot
```

Removing the marker alone does not stop an already running process. After reboot the launcher
waits without starting it. Ordinary Xiaozhi voice and Home Assistant remain separate from this
optional service. Keep the endpoint private, or delete `endpoint.txt` if no longer wanted; revoke
it in the cloud if it may have leaked.

To restore the previous rootfs, first disable the sidecar as above. While booted into the custom
slot, confirm with `slotctl status` that the other slot is still your known-good image, then run:

```sh
slotctl rollback
sync
reboot
```

`rollback` marks the booted slot bad and selects the other good slot; it does not reboot by itself.
If the trial cannot boot, the slot mechanism retries and falls back according to its trial policy;
use the existing rescue guide if necessary. `/data` survives rollback, which is why the enable
marker must be removed separately. Preserve the original good slot until all checks are complete.
