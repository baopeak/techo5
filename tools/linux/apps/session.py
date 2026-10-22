#!/usr/bin/env python3
"""Foreground local X11 session. No remote rendering or user shell commands."""
import ctypes
import os
import pathlib
import resource
import secrets
import signal
import stat
import subprocess
import sys
import tempfile
import time

from urllib.parse import parse_qsl, urlsplit
HOME = pathlib.Path('/home/browser')
APP = pathlib.Path('/usr/local/share/techo5-apps')
ENGINE = os.environ.get('TECHO5_TRIAL_BROWSER', 'firefox')
stop = False
children = []
private = None


def request_stop(_signum, _frame):
    global stop
    stop = True


for sig in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
    signal.signal(sig, request_stop)


def initial_browser_url(kind):
    raw = os.environ.get('TECHO5_APP_URL')
    if raw is None:
        return 'https://www.youtube.com/' if kind == 'youtube' else 'https://www.google.com/'
    try:
        if len(raw) > 8192:
            raise ValueError()
        url = urlsplit(raw)
        routes = {
            'www.google.com': {'/': None, '/search': 'q'},
            'www.youtube.com': {'/': None, '/results': 'search_query'},
            'www.netflix.com': {'/browse': None, '/search': 'q'},
        }
        if url.scheme != 'https' or url.netloc not in routes or url.fragment:
            raise ValueError()
        paths = routes[url.netloc]
        if url.path not in paths:
            raise ValueError()
        key = paths[url.path]
        pairs = parse_qsl(url.query, keep_blank_values=True, errors='strict')
        if key is None:
            if pairs or url.query:
                raise ValueError()
        elif len(pairs) != 1 or pairs[0][0] != key or not 2 <= len(pairs[0][1].strip()) <= 500:
            raise ValueError()
        return raw
    except (ValueError, UnicodeError):
        raise ValueError('unsupported app destination') from None


def notify_browser_ready():
    value = os.environ.get('TECHO5_APP_READY_FD')
    if value is None:
        return
    if value != '4' or not stat.S_ISFIFO(os.fstat(4).st_mode):
        raise RuntimeError('invalid app readiness channel')
    try:
        if os.write(4, b'ready\n') != 6:
            raise RuntimeError('app readiness channel ended')
    finally:
        os.close(4)


def limits():
    os.nice(5)
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_NOFILE, (512, 512))


def directory(parent, name, owner, mode):
    """Pin each component before changing it; never follow a user symlink."""
    try:
        os.mkdir(name, mode, dir_fd=parent)
        created = True
    except FileExistsError:
        created = False
    fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
    try:
        info = os.fstat(fd)
        if not created and info.st_uid != owner:
            raise RuntimeError('unsafe session directory owner')
        if created:
            os.fchown(fd, owner, owner)
        os.fchmod(fd, mode)
        return fd
    except BaseException:
        os.close(fd)
        raise


def prepare_directories():
    root = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        home = directory(root, 'home', 0, 0o755)
        try:
            os.close(directory(home, 'browser', 1000, 0o700))
        finally:
            os.close(home)
        run = directory(root, 'run', 0, 0o755)
        try:
            users = directory(run, 'user', 0, 0o755)
            try:
                os.close(directory(users, '1000', 1000, 0o700))
            finally:
                os.close(users)
        finally:
            os.close(run)
    finally:
        os.close(root)


def as_browser(action):
    """All writes beneath browser-owned directories happen after dropping root."""
    pid = os.fork()
    if pid == 0:
        try:
            os.setgroups([3003])
            os.setgid(1000)
            os.setuid(1000)
            action()
        except BaseException:
            os._exit(1)
        os._exit(0)
    _, status = os.waitpid(pid, 0)
    if status != 0:
        raise RuntimeError('browser session preparation failed')


def prepare_browser():
    home = os.open(HOME, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    runtime = os.open('/run/user/1000', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if ENGINE == 'chromium':
            os.close(directory(home, 'chromium-profile', 1000, 0o700))
            os.close(directory(runtime, 'pulse', 1000, 0o700))
            return
        profile = directory(home, 'profile', 1000, 0o700)
        try:
            fd = os.open('user.js', os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW,
                         0o600, dir_fd=profile)
            with os.fdopen(fd, 'w') as prefs:
                # Keep the content sandbox enabled; use real X11 touch events.
                prefs.write('user_pref("browser.warnOnQuit", false);\n'
                            'user_pref("browser.tabs.warnOnClose", false);\n'
                            'user_pref("gfx.webrender.software", true);\n'
                            'user_pref("dom.w3c_touch_events.enabled", 1);\n'
                            'user_pref("browser.shell.checkDefaultBrowser", false);\n'
                            'user_pref("media.eme.enabled", true);\n'
                            'user_pref("media.gmp-widevinecdm.enabled", true);\n'
                            'user_pref("media.gmp-widevinecdm.visible", true);\n'
                            'user_pref("media.gmp-widevinecdm.version", "4.10.2662.3");\n'
                            'user_pref("media.gmp-widevinecdm.abi", "aarch64-gcc3");\n'
                            'user_pref("media.gmp-widevinecdm.forceSupported", true);\n'
                            'user_pref("media.gmp-manager.updateEnabled", false);\n'
                            'user_pref("media.autoplay.default", 0);\n')
        finally:
            os.close(profile)
        os.close(directory(runtime, 'pulse', 1000, 0o700))
    finally:
        os.close(runtime)
        os.close(home)


def start(args, user=False, env=None, audio=False, pass_fds=()):
    kwargs = {}
    if user:
        kwargs = dict(user=1000, group=1000, extra_groups=[3003], preexec_fn=limits)
    proc = subprocess.Popen(args, env=env, stdin=subprocess.DEVNULL,
                            stdout=sys.stdout.buffer if audio else sys.stderr,
                            pass_fds=pass_fds, **kwargs)
    children.append(proc)
    return proc


def query(args, env, required=False):
    result = subprocess.run(args, env=env, user=1000, group=1000, extra_groups=[3003],
                            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, text=True, timeout=2)
    if required and result.returncode:
        raise RuntimeError('local window operation failed')
    return result.stdout.strip() if result.returncode == 0 else ''


def numbers(value):
    if '=' not in value:
        return []
    try:
        return [int(v.strip(), 0) for v in value.split('=', 1)[1].split(',')]
    except ValueError:
        return []


def wait_ready(check, watched, description, seconds):
    deadline = time.monotonic() + seconds
    while not stop and time.monotonic() < deadline:
        if any(p.poll() is not None for p in watched):
            raise RuntimeError(description + ' component exited')
        value = check()
        if value:
            return value
        time.sleep(.1)
    raise RuntimeError(description + ' not ready')


def maximize(window, env):
    """Ask the WM to maximize, so later dock-strut changes resize Firefox too."""
    def send():
        os.environ.clear()
        os.environ.update(env)
        x11 = ctypes.CDLL('libX11.so.6')
        x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
        x11.XOpenDisplay.restype = ctypes.c_void_p
        x11.XDefaultRootWindow.argtypes = [ctypes.c_void_p]
        x11.XDefaultRootWindow.restype = ctypes.c_ulong
        x11.XInternAtom.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int]
        x11.XInternAtom.restype = ctypes.c_ulong
        x11.XSendEvent.argtypes = [ctypes.c_void_p, ctypes.c_ulong, ctypes.c_int,
                                  ctypes.c_long, ctypes.c_void_p]
        x11.XCloseDisplay.argtypes = [ctypes.c_void_p]

        class ClientMessage(ctypes.Structure):
            _fields_ = [('type', ctypes.c_int), ('serial', ctypes.c_ulong),
                        ('send_event', ctypes.c_int), ('display', ctypes.c_void_p),
                        ('window', ctypes.c_ulong), ('message_type', ctypes.c_ulong),
                        ('format', ctypes.c_int), ('data', ctypes.c_long * 5)]

        class Event(ctypes.Union):
            _fields_ = [('client', ClientMessage), ('padding', ctypes.c_long * 24)]

        display = x11.XOpenDisplay(None)
        if not display:
            raise RuntimeError('display unavailable')
        try:
            atom = lambda name: x11.XInternAtom(display, name, False)
            event = Event()
            event.client.type = 33  # ClientMessage
            event.client.display = display
            event.client.window = int(window)
            event.client.message_type = atom(b'_NET_WM_STATE')
            event.client.format = 32
            event.client.data[:] = (1, atom(b'_NET_WM_STATE_MAXIMIZED_VERT'),
                                    atom(b'_NET_WM_STATE_MAXIMIZED_HORZ'), 1, 0)
            if not x11.XSendEvent(display, x11.XDefaultRootWindow(display), False,
                                 (1 << 20) | (1 << 19), ctypes.byref(event)):
                raise RuntimeError('maximize request failed')
        finally:
            x11.XCloseDisplay(display)
    as_browser(send)


def main():
    global private
    if len(sys.argv) != 2 or sys.argv[1] not in ('web', 'youtube'):
        raise ValueError('expected web or youtube')
    if ENGINE not in ('firefox', 'chromium'):
        raise ValueError('unsupported trial browser')
    initial_url = initial_browser_url(sys.argv[1])
    # Adopt orphaned grandchildren for cleanup, without creating child sessions.
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0:  # PR_SET_CHILD_SUBREAPER
        raise RuntimeError('cannot supervise browser descendants')
    prepare_directories()
    as_browser(prepare_browser)
    runtime = pathlib.Path('/run/user/1000')
    # Root-owned disposable state is outside every browser-writable directory.
    # GID 1000 may traverse/read authority, but cannot replace it or its lock files.
    private = pathlib.Path(tempfile.mkdtemp(prefix='techo5-session-', dir='/run'))
    os.chown(private, 0, 1000)
    private.chmod(0o710)
    auth = private / 'Xauthority'
    env = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': str(HOME), 'USER': 'browser',
           'LOGNAME': 'browser', 'DISPLAY': ':5', 'XAUTHORITY': str(auth),
           'XDG_RUNTIME_DIR': str(runtime), 'LANG': 'C.UTF-8', 'GDK_BACKEND': 'x11',
           'MOZ_GMP_PATH': '/opt/WidevineCdm/gmp-widevinecdm/latest',
           'MOZ_ENABLE_WAYLAND': '0', 'MOZ_USE_XINPUT2': '1', 'LIBGL_ALWAYS_SOFTWARE': '1',
           'PULSE_SERVER': 'unix:/run/user/1000/pulse/native'}
    result = subprocess.run(['/usr/bin/xauth', '-q', '-f', str(auth)], env=env,
                            input='add :5 . ' + secrets.token_hex(16) + '\n', text=True,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=3)
    if result.returncode:
        raise RuntimeError('display authorization preparation failed')
    fd = os.open(auth, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        os.fchown(fd, 0, 1000)
        os.fchmod(fd, 0o640)
    finally:
        os.close(fd)
    pulse = start(['/usr/bin/pulseaudio', '--daemonize=no', '--use-pid-file=no',
                   '--exit-idle-time=-1', '--log-target=stderr', '-n', '-F', str(APP / 'pulse.pa')],
                  user=True, env=env)
    wait_ready(lambda: query(['/usr/bin/pactl', 'info'], env), [pulse],
               'private browser audio', 4)
    # Only the synthetic sink monitor is captured. No physical microphone exists
    # in the app root; stdout is raw stereo PCM consumed by the native mixer.
    audio = start(['/usr/bin/parec', '--device=techo5.monitor', '--format=s16le',
                   '--rate=48000', '--channels=2', '--latency-msec=30'],
                  user=True, env=env, audio=True)
    xorg = start(['/usr/bin/Xorg', ':5', '-config', '/etc/X11/echo.conf',
                  '-logfile', str(private / 'Xorg.log'), '-nolisten', 'tcp',
                  '-keeptty', '-auth', str(auth), '-novtswitch', '-sharevts',
                  '-noreset', '-s', '0', '-dpms'], env=env)
    watched = [pulse, audio, xorg]
    wait_ready(lambda: query(['/usr/bin/xwininfo', '-root'], env), watched,
               'local display', 12)
    wm = start(['/usr/bin/openbox'], user=True, env=env)
    watched.append(wm)
    def wm_ready():
        value = query(['/usr/bin/xprop', '-root', '_NET_SUPPORTING_WM_CHECK'], env)
        try:
            return int(value.rsplit('#', 1)[1].strip(), 0) != 0
        except (ValueError, IndexError):
            return False

    wait_ready(wm_ready, watched, 'window manager', 6)
    keyboard_env = dict(env)
    keyboard_env['TECHO5_BROWSER_CLASS'] = ENGINE
    keyboard_env['TECHO5_APP_KIND'] = sys.argv[1]
    keyboard_fds = ()
    if os.environ.get('TECHO5_VIDEO_FD') == '3':
        os.fstat(3)  # Fail closed if the native supervisor did not supply its pipe.
        keyboard_env['TECHO5_VIDEO_FD'] = '3'
        keyboard_fds = (3,)
    if os.environ.get('TECHO5_DICTATION_FD') == '5':
        # Xiaozhi dictation for the keyboard's Nói button; the browser never inherits it.
        if not stat.S_ISSOCK(os.fstat(5).st_mode):
            raise RuntimeError('invalid dictation channel')
        keyboard_env['TECHO5_DICTATION_FD'] = '5'
        keyboard_fds += (5,)
    keyboard = start(['/usr/bin/python3', str(APP / 'keyboard.py')], user=True,
                     env=keyboard_env, pass_fds=keyboard_fds)
    watched.append(keyboard)

    def keyboard_ready():
        windows = query(['/usr/bin/xdotool', 'search', '--onlyvisible', '--class',
                         '^Techo5Keyboard$'], env).splitlines()
        if not windows or not windows[-1].isdigit():
            return None
        strut = numbers(query(['/usr/bin/xprop', '-id', windows[-1],
                               '_NET_WM_STRUT_PARTIAL'], env))
        area = numbers(query(['/usr/bin/xprop', '-root', '_NET_WORKAREA'], env))
        screen = query(['/usr/bin/xdotool', 'getdisplaygeometry'], env).split()
        if (len(strut) == 12 and strut[3] > 0 and len(area) >= 4 and
                len(screen) == 2 and all(v.isdigit() for v in screen) and
                area[2] > 1 and 1 < area[3] <= int(screen[1]) - strut[3]):
            return area[:4]
        return None

    area = wait_ready(keyboard_ready, watched, 'keyboard dock reservation', 10)
    url = initial_url
    # Cleanup stops Xorg alongside Chromium, so every close looks like a crash;
    # hide the resulting "Restore pages?" bubble that would cover each new page.
    browser_args = (['/usr/bin/chromium', '--disable-gpu', '--no-first-run',
                     '--no-default-browser-check', '--hide-crash-restore-bubble',
                     '--user-data-dir=' + str(HOME / 'chromium-profile'),
                     url] if ENGINE == 'chromium' else
                    ['/usr/bin/firefox-esr', '--no-remote', '--profile', str(HOME / 'profile'), url])
    firefox = start(browser_args, user=True, env=env)
    watched.append(firefox)

    def browser_window():
        windows = query(['/usr/bin/xdotool', 'search', '--onlyvisible', '--class',
                         ENGINE], env).splitlines()
        return windows[-1] if windows and windows[-1].isdigit() else None

    window = wait_ready(browser_window, watched, 'browser window', 25)
    area = wait_ready(keyboard_ready, watched, 'keyboard dock reservation', 3)
    # Firefox can first expose a 1x1 client. Seed usable geometry only after it
    # maps, then let EWMH maximization track the keyboard's dynamic work area.
    for args in (['windowsize', window, str(area[2]), str(area[3])],
                 ['windowmove', window, str(area[0]), str(area[1])],
                 ['windowactivate', window]):
        query(['/usr/bin/xdotool'] + args, env, required=True)
    maximize(window, env)

    def browser_ready():
        state = query(['/usr/bin/xprop', '-id', window, '_NET_WM_STATE'], env)
        if not all(name in state for name in ('_NET_WM_STATE_MAXIMIZED_VERT',
                                             '_NET_WM_STATE_MAXIMIZED_HORZ')):
            return False
        work = keyboard_ready()
        if not work:
            return False
        # xdotool doubles reparented titlebar offsets in the ARM64 runtime.
        # Use the X server's actual client coordinates for both runtimes.
        geometry = query(['/usr/bin/xwininfo', '-id', window], env)
        try:
            values = {line.split(':', 1)[0].strip(): line.split(':', 1)[1].strip()
                      for line in geometry.splitlines() if ':' in line}
            x, y, width, height = (int(values[key]) for key in
                                  ('Absolute upper-left X', 'Absolute upper-left Y', 'Width', 'Height'))
        except (KeyError, ValueError):
            return False
        return (width > 1 and height > 1 and x >= work[0] and y >= work[1] and
                x + width <= work[0] + work[2] and y + height <= work[1] + work[3])

    wait_ready(browser_ready, watched, 'browser usable work-area geometry', 5)
    notify_browser_ready()
    while not stop and all(p.poll() is None for p in watched):
        time.sleep(.1)
    if stop:
        return 0
    if any(p.poll() is not None for p in watched if p is not firefox):
        raise RuntimeError('mandatory local session component exited')
    return firefox.returncode or 0


def cleanup():
    # Terminate direct children first; the subreaper then adopts their survivors.
    # Re-read adopted PIDs on every pass: Firefox/Pulse can have grandchildren.
    deadline = time.monotonic() + 2
    while True:
        while True:
            try:
                pid, _ = os.waitpid(-1, os.WNOHANG)
            except ChildProcessError:
                return
            if pid == 0:
                break
        try:
            pids = pathlib.Path('/proc/self/task/%d/children' % os.getpid()).read_text().split()
        except OSError:
            pids = [str(p.pid) for p in children if p.returncode is None]
        for pid in pids:
            try:
                os.kill(int(pid), signal.SIGKILL if time.monotonic() >= deadline else signal.SIGTERM)
            except ProcessLookupError:
                pass
        time.sleep(.05)


if __name__ == '__main__':
    try:
        code = main()
    except Exception as exc:
        # Do not stringify subprocess errors: commands can contain destinations.
        detail = str(exc) if type(exc) is RuntimeError else type(exc).__name__
        print('Local browser session failed:', detail, file=sys.stderr)
        code = 1
    finally:
        cleanup()
        if private is not None:
            # Never open or follow entries below the root-owned authority directory.
            for entry in private.iterdir():
                entry.unlink()
            private.rmdir()
    sys.exit(code)
