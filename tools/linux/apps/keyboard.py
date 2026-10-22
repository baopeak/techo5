#!/usr/bin/env python3
"""Local X11 touchscreen keyboard for the TECHO5 Firefox session.

Run with the session's DISPLAY/XAUTHORITY as its unprivileged browser user:
    python3 /usr/local/share/techo5-apps/keyboard.py
Requires GTK3/PyGObject, GdkX11, xdotool and xprop; no OS input method.

Tap URL to select Firefox's address bar, compose locally, then Dán or Tìm/Enter.
For page search, first touch the page's search field. VI exposes vowel letters
and five tones; a tone replaces the tone of the last vowel in the current word.
For multi-vowel syllables, apply the tone immediately after its intended vowel
(e.g. t, i, ê, Sắc, n, g). This is explicit composition, not Telex or automatic
Vietnamese spelling correction. Backspace edits the local composition only.
The composition remains after pasting; Xóa clears it. Clipboard contents remain
available to Firefox until another application replaces them or this process
exits. Composition is not logged or persisted. Pasting sends it to the focused
Firefox field; submitting a web search naturally sends that search to the site.

Bàn phím hides/shows the large keys. The compact toolbar always reserves space
at the bottom through EWMH struts. Đóng and Escape request Firefox Ctrl+Q; the
owning launcher must wait for Firefox and clean up this process/Xorg afterward.
SIGTERM exits this overlay only (the launcher owns session shutdown). The dock
and all controls refuse input focus, leaving real Firefox fields focused.

For YouTube: search in the browser, open a public watch page, then tap Phát video.
The local player.py resolves streams via yt-dlp/QuickJS and hands H.264 up to
the height on the quality button (360p/480p/720p, 720p by default, remembered
in ~/.config/techo5/video-quality) to TECHO5's native framebuffer player.
Lower heights save software-decode CPU and network on the Echo.
Hủy mở video cancels a handoff that has not started yet. Resolution
requires network access to YouTube, without cookies/account or disk downloads.
Stream failures remain visible and do not close Firefox. SIGTERM also stops
and reaps the player; session process-group ownership remains with the launcher.

Primary layout is 1280x800; smaller screens use 48px keys. Grapheme deletion
handles combining marks, emoji modifiers, regional pairs and ZWJ sequences,
not every script-specific UAX #29 rule. Hardware/runtime verification belongs
to the deployment owner; this source does not imply a tested device session.
"""

import os
import signal
import subprocess
import sys
import unicodedata
from collections import deque

QUALITY_FILE = (".config", "techo5", "video-quality")


def quality_path():
    return os.path.join(os.environ.get("HOME") or os.path.expanduser("~"), *QUALITY_FILE)


def load_quality():
    """The remembered native-video height, or the player's default."""
    from player import quality_from
    try:
        with open(quality_path(), encoding="ascii") as handle:
            return quality_from(handle.read(16))
    except (OSError, UnicodeDecodeError):
        return quality_from(None)


def save_quality(height):
    """Remember the height; a read-only profile only loses the preference."""
    path = quality_path()
    try:
        os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
        temporary = path + ".tmp"
        with open(temporary, "w", encoding="ascii") as handle:
            handle.write("%d\n" % height)
        os.replace(temporary, path)
    except OSError:
        pass


TONE_MARKS = frozenset("\u0300\u0301\u0303\u0309\u0323")
VOWELS = frozenset("aeiouyAEIOUY")


def delete_grapheme(text):
    """Delete the final user-visible Vietnamese/combining or common emoji cluster."""
    if not text:
        return text
    clusters = []
    regional_run = 0
    for char in text:
        point = ord(char)
        regional = 0x1F1E6 <= point <= 0x1F1FF
        extending = (unicodedata.category(char) in ("Mn", "Mc", "Me")
                     or 0x1F3FB <= point <= 0x1F3FF
                     or char == "\u200d")
        if clusters and (extending or clusters[-1].endswith("\u200d")
                         or (regional and regional_run % 2 == 1)):
            clusters[-1] += char
        else:
            clusters.append(char)
        regional_run = regional_run + 1 if regional else 0
    return "".join(clusters[:-1])


def apply_tone(text, tone):
    """Replace the last vowel's tone within the final word; preserve vowel shape."""
    if tone and tone not in TONE_MARKS:
        raise ValueError("Unsupported tone")
    decomposed = unicodedata.normalize("NFD", text)
    end = len(decomposed)
    index = end - 1
    while index >= 0:
        char = decomposed[index]
        if char in VOWELS:
            stop = index + 1
            while stop < end and unicodedata.category(decomposed[stop]).startswith("M"):
                stop += 1
            marks = "".join(c for c in decomposed[index + 1:stop] if c not in TONE_MARKS)
            result = decomposed[:index + 1] + marks + tone + decomposed[stop:]
            return unicodedata.normalize("NFC", result)
        if not (char.isalpha() or unicodedata.category(char).startswith("M")):
            break
        index -= 1
    return text


def main():
    import gi
    gi.require_version("Gtk", "3.0")
    gi.require_version("GdkX11", "3.0")
    from gi.repository import Gdk, GdkX11, GLib, Gtk, Pango

    class Keyboard(Gtk.Window):
        def __init__(self):
            super().__init__(title="TECHO5 Bàn phím")
            self.set_wmclass("techo5-keyboard", "Techo5Keyboard")
            self.set_type_hint(Gdk.WindowTypeHint.DOCK)
            self.set_accept_focus(False)
            self.set_focus_on_map(False)
            self.set_decorated(False)
            self.set_resizable(False)
            self.set_keep_above(True)
            self.set_skip_taskbar_hint(True)
            self.set_skip_pager_hint(True)
            self.stick()
            self.buffer = ""
            self.secret = True
            self.upper = False
            self.vietnamese = True
            self.symbols = False
            self.expanded = True
            self.queue = deque()
            self.running = False
            self.child = None
            self.player = None
            self.player_stopping = False
            self.player_handoff = False
            self.reading_video = False
            self.video_request = 0
            self.quality = load_quality()
            self.closed = False
            self.screen = self.get_screen()
            self.width = self.screen.get_width()
            self.key_height = 48
            self.clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)
            self.connect("delete-event", self.on_delete)
            self.connect("destroy", self.on_destroy)
            self.connect("key-press-event", self.on_key)
            self.connect("size-allocate", self.on_allocate)
            self.screen.connect("size-changed", self.on_screen_changed)
            css = Gtk.CssProvider()
            css.load_from_data(b"""
                window { background: #17212b; color: #f4f7fb; }
                button { background: #2c3d4f; color: #ffffff; border: 1px solid #64788c;
                         border-radius: 5px; box-shadow: none; padding: 3px 7px;
                         font-family: Inter, 'Noto Sans', sans-serif; font-size: 18px; }
                button:active { background: #176a92; }
                button:disabled { color: #b0bdc9; background: #24313f; }
                label { font-family: Inter, 'Noto Sans', sans-serif; font-size: 18px; }
                .composition { background: #0c141e; color: #ffffff; padding: 5px; }
                .hint { color: #cbd9e6; font-size: 14px; }
            """)
            Gtk.StyleContext.add_provider_for_screen(self.screen, css, Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
            self.root = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=3)
            self.root.set_border_width(4)
            self.add(self.root)
            toolbar = Gtk.Box(spacing=4)
            self.root.pack_start(toolbar, False, False, 0)
            self.toggle = self.button("Ẩn phím", self.toggle_keyboard, toolbar)
            self.button("URL", lambda: self.send_keys("ctrl+l"), toolbar)
            self.button("Lùi", lambda: self.send_keys("alt+Left"), toolbar)
            self.button("Tiến", lambda: self.send_keys("alt+Right"), toolbar)
            self.button("Cuộn lên", lambda: self.scroll_page(4), toolbar)
            self.button("Cuộn xuống", lambda: self.scroll_page(5), toolbar)
            self.button("Netflix", self.youtube, toolbar)
            self.secret_button = self.button("Hiện chữ", self.toggle_secret, toolbar)
            self.play_button = self.button("Phát video", self.play_video, toolbar)
            self.stop_button = self.button("Hủy mở video", self.stop_video, toolbar)
            self.stop_button.set_sensitive(False)
            self.quality_button = self.button("%dp" % self.quality, self.cycle_quality, toolbar)
            native_youtube = (os.environ.get("TECHO5_APP_KIND", "youtube") == "youtube"
                              and os.environ.get("TECHO5_VIDEO_FD") == "3")
            self.play_button.set_no_show_all(not native_youtube)
            self.stop_button.set_no_show_all(not native_youtube)
            self.quality_button.set_no_show_all(not native_youtube)
            self.button("Đóng", self.close_browser, toolbar)
            self.panel = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=3)
            self.video_status = Gtk.Label(label=(
                "YouTube: phát trong trang hoặc bấm Phát video để mở native toàn màn hình"
                if native_youtube else "Phát trong trang web; bấm Đóng để về đồng hồ"))
            self.video_status.get_style_context().add_class("hint")
            self.root.pack_start(self.video_status, False, False, 0)
            self.root.pack_start(self.panel, False, False, 0)
            self.preview = Gtk.Label(xalign=0)
            self.preview.set_ellipsize(Pango.EllipsizeMode.NONE)
            self.preview.set_can_focus(False)
            self.preview.get_style_context().add_class("composition")
            self.scroll = Gtk.ScrolledWindow()
            self.scroll.set_policy(Gtk.PolicyType.AUTOMATIC, Gtk.PolicyType.NEVER)
            self.scroll.set_min_content_height(45)
            self.scroll.add(self.preview)
            self.panel.pack_start(self.scroll, False, False, 0)
            self.keybox = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=3)
            self.panel.pack_start(self.keybox, False, False, 0)
            self.hint = Gtk.Label(label="Chạm ô tìm kiếm → soạn ở đây → Dán / Tìm. Dấu đặt lên nguyên âm cuối.")
            self.hint.get_style_context().add_class("hint")
            self.panel.pack_start(self.hint, False, False, 0)
            self.rebuild_keys()
            self.update_preview()
            self.set_default_size(self.width, 1)
            self.show_all()
            GLib.idle_add(self.reserve_space)

        def button(self, text, callback, box):
            button = Gtk.Button(label=text)
            button.set_can_focus(False)
            button.set_focus_on_click(False)
            button.set_size_request(48, self.key_height)
            button.connect("clicked", lambda unused: callback())
            box.pack_start(button, True, True, 0)
            return button

        def rebuild_keys(self):
            for child in self.keybox.get_children():
                child.destroy()
            rows = (["1234567890", "@:/.-_?&=", "#+%!*(),;'"] if self.symbols
                    else ["qwertyuiop", "asdfghjkl", "zxcvbnm"])
            if self.vietnamese:
                accents = Gtk.Box(spacing=3)
                self.keybox.pack_start(accents, False, False, 0)
                for char in "ăâđêôơư":
                    value = char.upper() if self.upper else char
                    self.button(value, lambda value=value: self.append(value), accents)
                for name, tone in (("Sắc", "\u0301"), ("Huyền", "\u0300"),
                                   ("Hỏi", "\u0309"), ("Ngã", "\u0303"), ("Nặng", "\u0323")):
                    self.button(name, lambda tone=tone: self.tone(tone), accents)
                self.button("Bỏ dấu", lambda: self.tone(""), accents)
            for letters in rows:
                row = Gtk.Box(spacing=3)
                self.keybox.pack_start(row, False, False, 0)
                for char in letters:
                    value = char.upper() if self.upper else char
                    self.button(value, lambda value=value: self.append(value), row)
            actions = Gtk.Box(spacing=3)
            self.keybox.pack_start(actions, False, False, 0)
            self.button("VI" if self.vietnamese else "EN", self.toggle_language, actions)
            self.button("abc" if self.symbols else "123 / ?", self.toggle_symbols, actions)
            self.button("Chữ hoa" if not self.upper else "Chữ thường", self.toggle_case, actions)
            self.button("Cách", lambda: self.append(" "), actions)
            self.button("Xóa lùi", self.backspace, actions)
            self.button("Xóa", self.clear, actions)
            self.button("Dán", lambda: self.paste(False), actions)
            self.button("Tìm / Enter", lambda: self.paste(True), actions)
            self.keybox.show_all()
            self.resize(self.width, 1)

        def append(self, text):
            self.buffer = unicodedata.normalize("NFC", self.buffer + text)
            self.update_preview()

        def backspace(self):
            self.buffer = delete_grapheme(self.buffer)
            self.update_preview()

        def clear(self):
            self.buffer = ""
            self.update_preview()

        def tone(self, mark):
            self.buffer = apply_tone(self.buffer, mark)
            self.update_preview()

        def toggle_secret(self):
            self.secret = not self.secret
            self.secret_button.set_label("Hiện chữ" if self.secret else "Ẩn chữ")
            self.update_preview()

        def update_preview(self):
            text = ("•" * len(self.buffer)) if self.secret else self.buffer
            self.preview.set_text(text or "Soạn tiếng Việt / English tại đây")
            GLib.idle_add(self.scroll_to_end)

        def scroll_to_end(self):
            adjustment = self.scroll.get_hadjustment()
            adjustment.set_value(max(0, adjustment.get_upper() - adjustment.get_page_size()))
            return False

        def toggle_keyboard(self):
            self.expanded = not self.expanded
            self.panel.set_visible(self.expanded)
            self.toggle.set_label("Ẩn phím" if self.expanded else "Bàn phím")
            self.resize(self.width, 1)

        def toggle_language(self):
            self.vietnamese = not self.vietnamese
            self.rebuild_keys()

        def toggle_symbols(self):
            self.symbols = not self.symbols
            self.rebuild_keys()

        def toggle_case(self):
            self.upper = not self.upper
            self.rebuild_keys()

        def on_screen_changed(self, unused):
            self.width = self.screen.get_width()
            self.resize(self.width, 1)
            GLib.idle_add(self.reserve_space)

        def on_allocate(self, unused, allocation):
            self.move(0, self.screen.get_height() - allocation.height)
            if getattr(self, "reserved_height", None) != allocation.height:
                self.reserved_height = allocation.height
                GLib.idle_add(self.reserve_space)

        def reserve_space(self):
            window = self.get_window()
            if window is None or self.closed:
                return False
            xid = str(GdkX11.X11Window.get_xid(window))
            height = self.get_allocated_height()
            width = self.screen.get_width()
            partial = [0, 0, 0, height, 0, 0, 0, 0, 0, 0, 0, width - 1]
            self.enqueue([(["xprop", "-id", xid, "-f", "_NET_WM_STRUT_PARTIAL", "32c",
                            "-set", "_NET_WM_STRUT_PARTIAL", ", ".join(map(str, partial))], None),
                          (["xprop", "-id", xid, "-f", "_NET_WM_STRUT", "32c",
                            "-set", "_NET_WM_STRUT", "0, 0, 0, " + str(height)], None)])
            return False

        def enqueue(self, steps):
            self.queue.extend(steps)
            if not self.running:
                self.run_next()

        def run_next(self):
            if self.closed or not self.queue:
                self.running = False
                return False
            self.running = True
            command, callback = self.queue.popleft()
            if callable(command):
                command()
                GLib.timeout_add(120, self.run_next)
                return False
            try:
                self.child = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                              stdout=subprocess.PIPE if callback else subprocess.DEVNULL,
                                              stderr=subprocess.DEVNULL, close_fds=True)
            except OSError:
                self.queue.clear()
                self.running = False
                self.hint.set_text("Không chạy được công cụ X11. Kiểm tra xdotool / xprop.")
                return False
            GLib.timeout_add(20, self.poll_child, callback, GLib.get_monotonic_time())
            return False

        def poll_child(self, callback, started):
            if self.closed:
                return False
            status = self.child.poll()
            if status is None:
                if GLib.get_monotonic_time() - started < 3_000_000:
                    return True
                self.child.kill()
                self.child.wait()
                status = -1
            output = self.child.stdout.read() if self.child.stdout else b""
            if self.child.stdout:
                self.child.stdout.close()
            self.child = None
            if status != 0:
                self.queue.clear()
                self.hint.set_text("Chưa gửi được. Hãy chạm cửa sổ trình duyệt rồi thử lại.")
            elif callback:
                callback(output.decode("ascii", errors="ignore"))
            GLib.timeout_add(80, self.run_next)
            return False

        def browser_action(self, steps):
            # Target only the real browser class selected by the owning session.
            # Queue the search and its action atomically, including while another action runs.
            def found(output):
                windows = [line for line in output.splitlines() if line.isdecimal()]
                if not windows:
                    self.hint.set_text("Không tìm thấy cửa sổ trình duyệt.")
                    return
                actions = [(["xdotool", "windowactivate", "--sync", windows[-1]], None)] + steps
                self.queue.extendleft(reversed(actions))
            self.enqueue([(["xdotool", "search", "--onlyvisible", "--class",
                            os.environ.get("TECHO5_BROWSER_CLASS", "firefox")], found)])

        def send_keys(self, key):
            self.browser_action([(["xdotool", "key", "--clearmodifiers", key], None)])

        def scroll_page(self, wheel):
            # Wheel events target page content, not the focused search field.
            self.browser_action([
                (["xdotool", "key", "--clearmodifiers", "Escape"], None),
                (["xdotool", "mousemove", str(self.width // 2), "250"], None),
                (["xdotool", "click", "--repeat", "4", "--delay", "80", str(wheel)], None),
            ])

        def clipboard_step(self, text):
            def own_clipboard():
                self.clipboard.set_text(text, -1)
                # Do not call clipboard.store(): serving stays in this event loop,
                # and text is not intentionally handed to a persistence manager.
            return (own_clipboard, None)

        def paste(self, enter):
            steps = []
            if self.buffer:
                steps += [self.clipboard_step(self.buffer),
                          (["xdotool", "key", "--clearmodifiers", "ctrl+v"], None)]
            if enter:
                steps.append((["xdotool", "key", "--clearmodifiers", "Return"], None))
            if steps:
                self.browser_action(steps)
                self.clear()

        def youtube(self):
            self.browser_action([
                (["xdotool", "key", "--clearmodifiers", "ctrl+l"], None),
                self.clipboard_step("https://www.netflix.com/browse"),
                (["xdotool", "key", "--clearmodifiers", "ctrl+v"], None),
                (["xdotool", "key", "--clearmodifiers", "Return"], None),
            ])

        def play_video(self):
            if self.player is not None or self.reading_video:
                return
            if os.environ.get("TECHO5_VIDEO_FD") != "3":
                self.video_status.set_text("Trình phát native cần bản daemon mới có hỗ trợ chuyển video.")
                return
            self.reading_video = True
            self.video_request += 1
            token = self.video_request
            self.play_button.set_sensitive(False)
            self.video_status.set_text("Đang đọc địa chỉ video từ trình duyệt…")

            def received(clipboard, text, unused):
                if self.closed or token != self.video_request or not self.reading_video:
                    return
                if not text:
                    # Chromium can process Ctrl+L after the first Ctrl+C.
                    # Retry a fresh copy within the existing deadline.
                    GLib.timeout_add(100, copy_address)
                    return
                self.reading_video = False
                try:
                    from player import validate_url
                    url = validate_url(text)
                except (ImportError, ValueError, TypeError):
                    self.play_button.set_sensitive(True)
                    self.video_status.set_text("Hãy mở video youtube.com/watch hoặc youtu.be trước.")
                    return
                if self.expanded:
                    self.toggle_keyboard()
                try:
                    self.player = subprocess.Popen(
                        [sys.executable, os.path.join(os.path.dirname(os.path.abspath(__file__)), "player.py"),
                         "--max-height", str(self.quality), url], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                        stderr=subprocess.DEVNULL, close_fds=True, pass_fds=(3,))
                    os.set_blocking(self.player.stdout.fileno(), False)
                except OSError:
                    self.play_button.set_sensitive(True)
                    self.video_status.set_text("Không khởi động được trình phát video.")
                    return
                self.player_stopping = False
                self.player_handoff = False
                self.stop_button.set_sensitive(True)
                self.video_status.set_text("Đang lấy luồng YouTube… Hủy mở video để dừng.")
                GLib.timeout_add(200, self.poll_player)

            def request():
                if not self.closed and token == self.video_request and self.reading_video:
                    self.clipboard.request_text(received, None)

            def timeout():
                if self.reading_video and token == self.video_request:
                    self.reading_video = False
                    self.play_button.set_sensitive(True)
                    self.video_status.set_text("Không đọc được URL. Hãy chạm trình duyệt rồi thử lại.")
                return False

            def copy_address():
                if not self.closed and token == self.video_request and self.reading_video:
                    self.browser_action([
                        (["xdotool", "key", "--clearmodifiers", "ctrl+l"], None),
                        (["xdotool", "key", "--clearmodifiers", "ctrl+c"], None),
                        (["xdotool", "key", "--clearmodifiers", "Escape"], None),
                        (request, None),
                    ])
                return False

            self.browser_action([
                self.clipboard_step(""),
                (copy_address, None),
            ])
            GLib.timeout_add_seconds(6, timeout)

        def cycle_quality(self):
            from player import next_quality
            self.quality = next_quality(self.quality)
            save_quality(self.quality)
            self.quality_button.set_label("%dp" % self.quality)
            notes = {360: "nhẹ CPU và mạng nhất", 480: "nhẹ hơn, tiết kiệm mạng",
                     720: "mặc định, nét nhất và vẫn mượt trên Echo"}
            self.video_status.set_text("Chất lượng Phát video: %dp (%s). Áp dụng lần phát tiếp theo."
                                       % (self.quality, notes[self.quality]))

        def stop_video(self):
            if self.player is not None and self.player.poll() is None:
                self.stop_button.set_sensitive(False)
                self.player_stopping = True
                self.player.terminate()
                self.video_status.set_text("Đang dừng video…")

        def poll_player(self):
            if self.closed or self.player is None:
                return False
            try:
                progress = os.read(self.player.stdout.fileno(), 256)
            except BlockingIOError:
                progress = b""
            if b"NATIVE_HANDOFF" in progress and not self.player_stopping:
                self.player_handoff = True
                self.video_status.set_text("Đang chuyển sang trình phát native của Echo…")
            status = self.player.poll()
            if status is None:
                return True
            self.player.wait()
            self.player.stdout.close()
            self.player = None
            self.play_button.set_sensitive(True)
            self.stop_button.set_sensitive(False)
            if status == 0 and not self.player_stopping:
                self.player_handoff = True
                self.play_button.set_sensitive(False)
                self.video_status.set_text("Đang chuyển sang trình phát native của Echo…")
            elif self.player_stopping:
                self.video_status.set_text("Đã hủy mở video. Tiếp tục dùng Firefox.")
            else:
                self.video_status.set_text("Không phát được video công khai tương thích. Firefox vẫn mở.")
            self.player_stopping = False
            if not self.player_handoff:
                self.browser_action([])
            return False

        def close_browser(self):
            self.stop_video()
            self.send_keys("alt+F4" if os.environ.get("TECHO5_BROWSER_CLASS") == "chromium" else "ctrl+q")

        def on_key(self, unused, event):
            if event.keyval == Gdk.KEY_Escape:
                self.close_browser()
                return True
            return False

        def on_delete(self, unused, event):
            self.close_browser()
            return True

        def on_destroy(self, unused):
            self.closed = True
            if self.player is not None:
                if self.player.poll() is None:
                    self.player.terminate()
                    try:
                        self.player.wait(timeout=4)
                    except subprocess.TimeoutExpired:
                        self.player.kill()
                        self.player.wait()
                else:
                    self.player.wait()
                if self.player.stdout:
                    self.player.stdout.close()
            if self.child and self.child.poll() is None:
                self.child.kill()
                self.child.wait()
            Gtk.main_quit()

    if not os.environ.get("DISPLAY"):
        sys.stderr.write("TECHO5 keyboard requires a local X11 DISPLAY.\n")
        return 1
    keyboard = Keyboard()
    GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, signal.SIGTERM, lambda: (keyboard.destroy(), False)[1])
    GLib.unix_signal_add(GLib.PRIORITY_DEFAULT, signal.SIGINT, lambda: (keyboard.destroy(), False)[1])
    Gtk.main()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
