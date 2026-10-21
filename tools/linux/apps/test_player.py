"""Pure player selection/URL tests; no network, GTK or subprocesses.

Run by deployment owner: python3 -m unittest discover -s tools/linux/apps.
"""
import unittest

from player import select_formats, validate_url


class UrlTests(unittest.TestCase):
    def test_canonical_watch_and_short_urls(self):
        expected = "https://www.youtube.com/watch?v=abcdefgh_-1"
        for value in (expected, "https://youtube.com/watch?v=abcdefgh_-1&t=20",
                      "https://m.youtube.com/watch?v=abcdefgh_-1",
                      "https://youtu.be/abcdefgh_-1?si=ignored"):
            self.assertEqual(validate_url(value), expected)

    def test_only_real_https_youtube_video_urls(self):
        for value in (None, "", "http://youtu.be/abcdefgh_-1",
                      "https://www.youtube.com/", "https://youtube.com/results?search_query=x",
                      "https://youtube.com.evil.test/watch?v=abcdefgh_-1",
                      "https://evil.test/watch?v=abcdefgh_-1",
                      "https://user@youtube.com/watch?v=abcdefgh_-1",
                      "https://youtube.com:8443/watch?v=abcdefgh_-1",
                      "https://youtube.com:bad/watch?v=abcdefgh_-1",
                      "https://youtube.com/watch?v=abcdefgh_-1&v=abcdefgh_-2",
                      "https://youtu.be/short", "https://youtu.be/abcdefgh_-1/extra",
                      "https://youtu.be/abcdefgh_;1", "file:///tmp/video"):
            with self.subTest(value=value), self.assertRaises((ValueError, TypeError)):
                validate_url(value)


class FormatTests(unittest.TestCase):
    def video(self, name, height, codec="avc1.4d401e", audio="none"):
        return {"url": "https://media.example/" + name, "height": height,
                "vcodec": codec, "acodec": audio, "fps": 30}

    def audio(self, name, codec="mp4a.40.2", bitrate=128):
        return {"url": "https://media.example/" + name, "vcodec": "none",
                "acodec": codec, "abr": bitrate}

    def test_select_h264_at_most_480_and_aac(self):
        formats = [self.video("360", 360), self.video("480", 480),
                   self.video("720", 720), self.video("vp9", 480, "vp9"),
                   self.audio("opus", "opus", 160), self.audio("aac")]
        self.assertEqual(select_formats({"formats": formats}),
                         ("https://media.example/480", "https://media.example/aac"))

    def test_muxed_audio_needs_no_extra_stream(self):
        self.assertEqual(select_formats({"formats": [self.video("muxed", 360, audio="mp4a.40.2")]}),
                         ("https://media.example/muxed", None))

    def test_opus_when_aac_unavailable(self):
        self.assertEqual(select_formats({"formats": [self.video("v", 480), self.audio("a", "opus")]}),
                         ("https://media.example/v", "https://media.example/a"))

    def test_missing_compatible_streams_fails(self):
        for formats in ([], [self.video("v", 480)], [self.video("v", 720), self.audio("a")],
                        [self.video("v", 480, "vp9"), self.audio("a")],
                        [{**self.video("v", 480), "url": "file:///tmp/v"}, self.audio("a")]):
            with self.subTest(formats=formats), self.assertRaises(ValueError):
                select_formats({"formats": formats})


if __name__ == "__main__":
    unittest.main()
