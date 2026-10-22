#!/usr/bin/env python3
"""Extract a public YouTube URL locally and hand it to TECHO5's native player.

Invoked by keyboard.py as `player.py [--max-height 360|480|720] URL` with one
HTTPS YouTube URL. No cookies, downloads, shell, external rendering or X11
video decoder. QuickJS/yt-dlp select H.264 up to the chosen height (480p by
default; the Echo decodes in software, so 720p may drop frames) and AAC/Opus. The inherited fd3 carries one bounded native-video request;
stdout carries only a fixed status token, never signed media URLs.
Exit 0 means handoff sent, 2 invalid URL, 3 extraction failure, 4 unavailable
handoff. Extraction has a 75-second deadline; cancellation reaps its child.
"""

import json
import os
import re
import signal
import stat
import subprocess
import sys
from urllib.parse import parse_qs, urlsplit


VIDEO_ID = re.compile(r"[A-Za-z0-9_-]{11}\Z")
# Heights the user can pick for native playback; 480p measured smooth on crown.
QUALITIES = (360, 480, 720)
DEFAULT_QUALITY = 480


def quality_from(value):
    """Return a supported height from untrusted text, else the default."""
    try:
        height = int(str(value).strip())
    except (TypeError, ValueError):
        return DEFAULT_QUALITY
    return height if height in QUALITIES else DEFAULT_QUALITY


def next_quality(height):
    """The next height in the cycle shown by the keyboard's quality button."""
    index = QUALITIES.index(quality_from(height))
    return QUALITIES[(index + 1) % len(QUALITIES)]


def parse_args(argv):
    """Return (extracting, max_height, url) or None for a malformed command line."""
    argv = list(argv)
    extracting = bool(argv and argv[0] == "--extract")
    if extracting:
        argv = argv[1:]
    max_height = DEFAULT_QUALITY
    if argv and argv[0] == "--max-height":
        if len(argv) < 2:
            return None
        max_height = quality_from(argv[1])
        argv = argv[2:]
    if len(argv) != 1 or argv[0].startswith("--"):
        return None
    return extracting, max_height, argv[0]


def validate_url(value):
    """Return a canonical public YouTube watch URL; reject credentials/ports."""
    if not isinstance(value, str) or len(value) > 4096:
        raise ValueError("Invalid video URL")
    parsed = urlsplit(value)
    if (parsed.scheme != "https" or parsed.username is not None
            or parsed.password is not None or parsed.port not in (None, 443)):
        raise ValueError("Invalid video URL")
    host = parsed.hostname
    if host in ("youtube.com", "www.youtube.com", "m.youtube.com") and parsed.path == "/watch":
        values = parse_qs(parsed.query).get("v", [])
        video_id = values[0] if len(values) == 1 else ""
    elif host == "youtu.be":
        video_id = parsed.path[1:]
    else:
        raise ValueError("Invalid video URL")
    if not VIDEO_ID.fullmatch(video_id):
        raise ValueError("Invalid video URL")
    return "https://www.youtube.com/watch?v=" + video_id


def select_formats(info, max_height=DEFAULT_QUALITY):
    """Choose software-decodable H.264 up to max_height and a compatible audio stream."""
    formats = info.get("formats", [])
    videos = []
    audios = []
    for item in formats:
        url = item.get("url", "")
        if not isinstance(url, str) or urlsplit(url).scheme != "https":
            continue
        video = str(item.get("vcodec") or "none")
        audio = str(item.get("acodec") or "none")
        height = item.get("height") or 0
        if video.startswith(("avc1", "h264")) and 0 < height <= max_height:
            videos.append(item)
        if video == "none" and audio.startswith(("mp4a", "aac", "opus")):
            audios.append(item)
    videos = [item for item in videos if audios or
              str(item.get("acodec") or "none").startswith(("mp4a", "aac", "opus"))]
    if not videos:
        raise ValueError("No compatible video")
    # Tallest first; at one height prefer <=30 fps, since the native player shows at
    # most 30 and a 60 fps twin only doubles the software decode.
    chosen = max(videos, key=lambda item: (item.get("height") or 0,
                                           (item.get("fps") or 0) <= 30.5,
                                           item.get("fps") or 0,
                                           item.get("tbr") or 0))
    if str(chosen.get("acodec") or "none").startswith(("mp4a", "aac", "opus")):
        return chosen["url"], None
    if not audios:
        raise ValueError("No compatible audio")
    # Prefer AAC for this CPU; Opus is supported when AAC is unavailable.
    audio = max(audios, key=lambda item: (str(item.get("acodec")).startswith(("mp4a", "aac")),
                                         item.get("abr") or item.get("tbr") or 0))
    return chosen["url"], audio["url"]


def extract(url, max_height=DEFAULT_QUALITY):
    import yt_dlp

    class QuietLogger:
        def debug(self, message):
            pass

        info = debug
        warning = debug
        error = debug

    options = {"quiet": True, "no_warnings": True, "logger": QuietLogger(),
               "noplaylist": True, "skip_download": True, "cachedir": False,
               "socket_timeout": 15, "retries": 1, "extractor_retries": 1,
               "js_runtimes": {"quickjs": {}}, "remote_components": set()}
    with yt_dlp.YoutubeDL(options) as downloader:
        info = downloader.extract_info(url, download=False)
    return select_formats(info, max_height)


def main(argv):
    parsed = parse_args(argv)
    if parsed is None:
        return 2
    extracting, max_height, raw_url = parsed
    try:
        url = validate_url(raw_url)
    except (ValueError, TypeError):
        return 2

    def stop(signum, frame):
        # A second Stop must not interrupt terminate/wait/kill/reap.
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    if extracting:
        try:
            sys.stdout.write(json.dumps(extract(url, max_height)))
            return 0
        except Exception:
            return 3
    try:
        if os.environ.get("TECHO5_VIDEO_FD") != "3" or not stat.S_ISFIFO(os.fstat(3).st_mode):
            return 4
    except OSError:
        return 4

    child = None
    try:
        child = subprocess.Popen([sys.executable, __file__, "--extract",
                                  "--max-height", str(max_height), url],
                                 stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL)
        try:
            data, unused = child.communicate(timeout=75)
        except subprocess.TimeoutExpired:
            return 3
        if child.returncode != 0:
            return 3
        try:
            video, audio = json.loads(data)
        except (ValueError, TypeError):
            return 3
        request = json.dumps({"url": video, "audio_url": audio or ""},
                             separators=(",", ":")).encode() + b"\n"
        if len(request) > 64 * 1024:
            return 3
        # No shared framebuffer writer: native playback starts only after the
        # supervisor has reaped Xorg and the daemon invalidates its page caches.
        with os.fdopen(3, "wb") as channel:
            channel.write(request)
        print("NATIVE_HANDOFF", flush=True)
        return 0
    except (OSError, ValueError):
        return 4
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        if child is not None:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
            if child.stdout:
                child.stdout.close()


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
