#!/usr/bin/env python3
"""Play a public YouTube watch URL locally with yt-dlp and software X11 mpv.

Invoked by keyboard.py with one HTTPS YouTube URL and optional --geometry.
No cookies, account, persistent download, shell, or external rendering. The
extractor uses QuickJS and selects H.264 <=480p plus AAC/Opus audio. Missing
compatible formats are an error, not an untested codec fallback. All diagnostic
output is suppressed to avoid exposing signed media URLs. Exit 0 means mpv
finished normally, 2 invalid URL, 3 extraction/format failure, 4 player failure.
SIGTERM/SIGINT terminate and reap the owned child; no process-group signals.
Extraction has a 75-second deadline. The launcher must monitor keyboard.py and
own the surrounding session process group. Hardware playback is not implied.
"""

import json
import re
import signal
import subprocess
import sys
from urllib.parse import parse_qs, urlsplit


VIDEO_ID = re.compile(r"[A-Za-z0-9_-]{11}\Z")


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


def select_formats(info):
    """Choose software-decodable <=480p H.264 and a compatible audio stream."""
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
        if video.startswith(("avc1", "h264")) and 0 < height <= 480:
            videos.append(item)
        if video == "none" and audio.startswith(("mp4a", "aac", "opus")):
            audios.append(item)
    videos = [item for item in videos if audios or
              str(item.get("acodec") or "none").startswith(("mp4a", "aac", "opus"))]
    if not videos:
        raise ValueError("No compatible video")
    chosen = max(videos, key=lambda item: (item.get("height") or 0,
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


def extract(url):
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
    return select_formats(info)


def main(argv):
    extracting = bool(argv and argv[0] == "--extract")
    if extracting:
        argv = argv[1:]
    if not argv or len(argv) > (1 if extracting else 2):
        return 2
    try:
        url = validate_url(argv[0])
    except (ValueError, TypeError):
        return 2
    def stop(signum, frame):
        # Cancellation is idempotent, including while terminating/reaping mpv.
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        # Raise through Python context managers, including yt-dlp's JS runner,
        # so extraction cancellation can clean up its own runtime subprocess.
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    if extracting:
        try:
            result = extract(url)
            sys.stdout.write(json.dumps(result))
            return 0
        except Exception:
            return 3
    geometry = argv[1] if len(argv) == 2 else "1280x660+0+0"
    if not re.fullmatch(r"\d{2,4}x\d{2,4}\+0\+0", geometry):
        return 2
    child = None

    try:
        child = subprocess.Popen([sys.executable, __file__, "--extract", url],
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
        command = ["mpv", "--no-config", "--vo=x11", "--hwdec=no", "--ao=pulse",
                   "--keep-open=no", "--title=TECHO5Video", "--geometry=" + geometry,
                   "--autofit=" + geometry.split("+")[0], "--ytdl=no", "--no-terminal",
                   "--input-default-bindings=yes", "--osd-level=1"]
        if audio:
            command.append("--audio-file=" + audio)
        command += ["--", video]
        child = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # A fixed token only: never expose extracted URLs or exception text.
        print("PLAYING", flush=True)
        return 0 if child.wait() == 0 else 4
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
