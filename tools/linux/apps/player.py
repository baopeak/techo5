#!/usr/bin/env python3
"""Extract a public YouTube URL locally and hand it to TECHO5's native player.

Invoked by keyboard.py as `player.py [--max-height 360|480|720] URL` with one
HTTPS YouTube URL, or `... --search WORDS` to play the YouTube result best matching
a title Xiaozhi heard (play_youtube), searched with and without Vietnamese marks. No downloads, shell, external rendering or X11 video
decoder. When the browser profile is signed in, its own cookies are read (never
copied out or logged), since YouTube asks anonymous extraction from a flagged
address to sign in. yt-dlp, solving YouTube's JS challenge with Node when
installed (QuickJS takes over a minute on crown), selects H.264 up to the chosen
height (720p by default, preferring <=30 fps) and AAC/Opus. The inherited fd3 carries one bounded native-video request;
stdout carries only a fixed status token, never signed media URLs.
Exit 0 means handoff sent, 2 invalid URL, 3 extraction failure, 4 unavailable
handoff, 5 YouTube asked to sign in. Extraction has a 120-second deadline (room
for QuickJS); cancellation reaps its child.
"""

import difflib
import json
import os
import re
import signal
import stat
import subprocess
import sys
import unicodedata
from urllib.parse import parse_qs, urlsplit


VIDEO_ID = re.compile(r"[A-Za-z0-9_-]{11}\Z")
# Heights the user can pick for native playback. 720p (60 fps sources shown at
# 30) measured smooth on crown; 1080p decodes too slowly for the panel's gain.
QUALITIES = (360, 480, 720)
DEFAULT_QUALITY = 720
# The Chromium profile session.py starts the browser with.
BROWSER_PROFILE = "/home/browser/chromium-profile"
# Exit status for YouTube refusing extraction until someone signs in.
SIGN_IN = 5


def needs_sign_in(error):
    """Whether YouTube refused as it does an address it takes for a bot, or a signed-out session."""
    text = str(error)
    return "Sign in to confirm" in text or "--cookies" in text


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
    """Return (extracting, max_height, searching, target) or None for a malformed command line.

    target is a watch URL, or with --search the words whose first YouTube result plays."""
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
    searching = bool(argv and argv[0] == "--search")
    if searching:
        argv = argv[1:]
    if len(argv) != 1 or argv[0].startswith("--"):
        return None
    return extracting, max_height, searching, argv[0]


def validate_query(value):
    """Return search words for yt-dlp's ytsearch: 2-500 printable characters."""
    if not isinstance(value, str):
        raise ValueError("Invalid search")
    value = " ".join(value.split())
    if not 2 <= len(value) <= 500 or not value.isprintable():
        raise ValueError("Invalid search")
    return value


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


def extractor_options(profile=BROWSER_PROFILE):
    """yt-dlp options: quiet, no cache, Node before QuickJS, the browser's cookies if it has any."""
    options = {"quiet": True, "no_warnings": True, "noplaylist": True,
               "skip_download": True, "cachedir": False, "logger": QuietLogger(),
               "socket_timeout": 15, "retries": 1, "extractor_retries": 1,
               "js_runtimes": {"node": {}, "quickjs": {}}, "remote_components": set()}
    if os.path.isfile(os.path.join(profile, "Default", "Cookies")):
        # No keyring runs in the app root, so Chromium stores them with its basic key.
        options["cookiesfrombrowser"] = ("chromium", profile, "BASICTEXT", None)
    return options


class QuietLogger:
    """yt-dlp logger that keeps signed URLs and errors out of every log."""

    def debug(self, message):
        pass

    info = debug
    warning = debug
    error = debug


def plain_words(text):
    """Lowercase words without Vietnamese marks or đ: speech recognition slips most in the marks."""
    text = unicodedata.normalize("NFD", text.replace("đ", "d").replace("Đ", "D"))
    return re.findall(r"\w+", "".join(c for c in text if unicodedata.category(c) != "Mn").lower())


def title_score(query, title):
    """How well a title matches the words asked for, marks aside; runs of them in order count most."""
    matcher = difflib.SequenceMatcher(None, plain_words(query), plain_words(title), autojunk=False)
    return sum(block.size * block.size for block in matcher.get_matching_blocks())


def best_result(query, entries):
    """The result whose title matches query best, earlier results winning ties; else the first."""
    entries = [entry for entry in entries if VIDEO_ID.fullmatch(str(entry.get("id") or ""))]
    if not entries:
        raise ValueError("No search result")
    best = max(entries, key=lambda entry: title_score(query, str(entry.get("title") or "")))
    return best if title_score(query, str(best.get("title") or "")) else entries[0]


def find_video(query):
    """The watch URL best matching query among YouTube's first results for it, with and without marks.

    A title heard with a wrong mark ("chuyến đêm muôn") misses the video searched as is, but
    finds it searched without marks; the title match then picks it from either list."""
    import yt_dlp

    options = extractor_options()
    options["extract_flat"] = "in_playlist"
    entries = []
    with yt_dlp.YoutubeDL(options) as downloader:
        for words in dict.fromkeys([query, " ".join(plain_words(query))]):
            info = downloader.extract_info("ytsearch5:" + words, download=False)
            entries += [entry for entry in info.get("entries") or [] if entry]
    return "https://www.youtube.com/watch?v=" + best_result(query, entries)["id"]


def extract(url, max_height=DEFAULT_QUALITY):
    import yt_dlp

    with yt_dlp.YoutubeDL(extractor_options()) as downloader:
        info = downloader.extract_info(url, download=False)
    return select_formats(info, max_height)


def main(argv):
    parsed = parse_args(argv)
    if parsed is None:
        return 2
    extracting, max_height, searching, target = parsed
    try:
        query = validate_query(target) if searching else None
        url = None if searching else validate_url(target)
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
            sys.stdout.write(json.dumps(extract(find_video(query) if searching else url, max_height)))
            return 0
        except Exception as error:
            return SIGN_IN if needs_sign_in(error) else 3
    try:
        if os.environ.get("TECHO5_VIDEO_FD") != "3" or not stat.S_ISFIFO(os.fstat(3).st_mode):
            return 4
    except OSError:
        return 4

    child = None
    try:
        child = subprocess.Popen([sys.executable, __file__, "--extract", "--max-height", str(max_height)]
                                 + (["--search", target] if searching else [url]),
                                 stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL)
        try:
            data, unused = child.communicate(timeout=120)
        except subprocess.TimeoutExpired:
            return 3
        if child.returncode == SIGN_IN:
            return SIGN_IN
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
