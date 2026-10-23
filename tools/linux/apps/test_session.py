"""Behavioral boundaries for model-requested browser destinations and readiness."""
import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

if os.name != 'posix':
    raise unittest.SkipTest('session supervisor requires POSIX resource and inherited descriptors')

spec = importlib.util.spec_from_file_location('app_session', Path(__file__).with_name('session.py'))
session = importlib.util.module_from_spec(spec)
spec.loader.exec_module(session)


class SessionControlTests(unittest.TestCase):
    def test_vietnamese_search_and_defaults(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(session.initial_browser_url('web'), 'https://www.google.com/')
            self.assertEqual(session.initial_browser_url('youtube'), 'https://www.youtube.com/')
        for address in [
            'https://www.google.com/search?q=%C4%90%C3%A0+L%E1%BA%A1t',
            'https://www.youtube.com/results?search_query=travel+Vietnam',
            'https://www.netflix.com/search?q=%C4%90%C3%A0+L%E1%BA%A1t',
            'https://www.netflix.com/browse',
        ]:
            with self.subTest(address=address), patch.dict(os.environ, {'TECHO5_APP_URL': address}):
                self.assertEqual(session.initial_browser_url('web'), address)

    def test_model_cannot_select_arbitrary_navigation(self):
        for address in [
            'file:///etc/passwd', 'javascript:alert(1)', 'http://www.google.com/',
            'https://www.google.com.evil.test/search?q=hello',
            'https://user@www.google.com/search?q=hello',
            'https://www.google.com:443/search?q=hello',
            'https://www.google.com/search?q=hello#fragment',
            'https://www.google.com/search?q=hello&q=other',
            'https://www.youtube.com/watch?v=not-an-allowed-tool-route',
            'https://www.netflix.com/account',
            'https://www.google.com/search?q=a',
            'https://www.google.com/search?q=' + 'a' * 501,
        ]:
            with self.subTest(address=address), patch.dict(os.environ, {'TECHO5_APP_URL': address}):
                with self.assertRaises(ValueError):
                    session.initial_browser_url('web')

    def test_only_a_requested_play_of_youtube_results_autoplays(self):
        results = 'https://www.youtube.com/results?search_query=chuy%E1%BB%87n+%C4%91%C3%AAm+mu%E1%BB%99n+t%E1%BA%ADp+14'
        with patch.dict(os.environ, {'TECHO5_APP_PLAY': '1'}):
            self.assertEqual(session.play_query(results), 'chuyện đêm muộn tập 14')
            self.assertIsNone(session.play_query('https://www.google.com/search?q=hello'))
            self.assertIsNone(session.play_query('https://www.youtube.com/'))
        with patch.dict(os.environ, {}, clear=True):
            self.assertIsNone(session.play_query(results))

    @unittest.skipUnless(os.name == 'posix', 'real inherited-fd behavior requires POSIX')
    def test_ready_acknowledges_and_closes_real_pipe(self):
        read_fd, write_fd = os.pipe()
        original_pipe = (read_fd, write_fd)
        saved = None
        try:
            if 4 not in original_pipe:
                try:
                    saved = os.dup(4)
                except OSError:
                    pass
            if read_fd == 4:
                read_fd = os.dup(read_fd)
            os.dup2(write_fd, 4)
            if write_fd != 4:
                os.close(write_fd)
            write_fd = None
            with patch.dict(os.environ, {'TECHO5_APP_READY_FD': '4'}):
                session.notify_browser_ready()
            self.assertEqual(os.read(read_fd, 32), b'ready\n')
            self.assertEqual(os.read(read_fd, 1), b'')
        finally:
            if write_fd is not None:
                os.close(write_fd)
            os.close(read_fd)
            if saved is not None:
                os.dup2(saved, 4)
                os.close(saved)


if __name__ == '__main__':
    unittest.main()
