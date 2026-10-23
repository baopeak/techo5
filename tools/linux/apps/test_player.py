"""Pure player selection/URL tests; no network, GTK or subprocesses.

Run by deployment owner: python3 -m unittest discover -s tools/linux/apps.
"""
import os
import tempfile
import unittest

from player import (QUALITIES, extractor_options, needs_sign_in, next_quality, parse_args,
                    quality_from, select_formats, validate_query, validate_url)


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

    def test_default_selects_h264_at_most_720_and_aac(self):
        formats = [self.video("360", 360), self.video("480", 480),
                   self.video("720", 720), self.video("1080", 1080), self.video("vp9", 720, "vp9"),
                   self.audio("opus", "opus", 160), self.audio("aac")]
        self.assertEqual(select_formats({"formats": formats}),
                         ("https://media.example/720", "https://media.example/aac"))

    def test_quality_cap_selects_highest_allowed_height(self):
        formats = [self.video("360", 360), self.video("480", 480),
                   self.video("720", 720), self.video("1080", 1080), self.audio("aac")]
        for cap, expected in ((360, "360"), (480, "480"), (720, "720")):
            with self.subTest(cap=cap):
                self.assertEqual(select_formats({"formats": formats}, cap)[0],
                                 "https://media.example/" + expected)

    def test_prefers_30fps_or_less_at_the_same_height(self):
        # The native player shows at most 30 frames a second; a 60 fps twin costs twice the decode.
        formats = [{**self.video("720p60", 720), "fps": 60}, self.video("720p30", 720),
                   {**self.video("480p60", 480), "fps": 60}, self.audio("aac")]
        self.assertEqual(select_formats({"formats": formats}, 720)[0], "https://media.example/720p30")
        only_60 = [{**self.video("720p60", 720), "fps": 60}, self.video("480p30", 480), self.audio("aac")]
        self.assertEqual(select_formats({"formats": only_60}, 720)[0], "https://media.example/720p60")

    def test_quality_cap_falls_back_to_lower_stream(self):
        formats = [self.video("360", 360), self.audio("aac")]
        self.assertEqual(select_formats({"formats": formats}, 720)[0], "https://media.example/360")

    def test_quality_values_are_bounded(self):
        self.assertEqual(QUALITIES, (360, 480, 720))
        for value, expected in (("720", 720), (360, 360), ("480\n", 480), ("1080", 720),
                                ("", 720), (None, 720), ("abc", 720), ("-1", 720)):
            with self.subTest(value=value):
                self.assertEqual(quality_from(value), expected)

    def test_arguments_carry_quality_to_extractor(self):
        url = "https://youtu.be/abcdefgh_-1"
        self.assertEqual(parse_args([url]), (False, 720, False, url))
        self.assertEqual(parse_args(["--max-height", "720", url]), (False, 720, False, url))
        self.assertEqual(parse_args(["--extract", "--max-height", "360", url]), (True, 360, False, url))
        self.assertEqual(parse_args(["--max-height", "9999", url]), (False, 720, False, url))
        self.assertEqual(parse_args(["--max-height", "720", "--search", "chuyện đêm muộn tập 14"]),
                         (False, 720, True, "chuyện đêm muộn tập 14"))
        self.assertEqual(parse_args(["--extract", "--max-height", "480", "--search", "x y"]),
                         (True, 480, True, "x y"))
        for argv in ([], ["--max-height", url], [url, url], ["--max-height"], ["--search"],
                     ["--search", "--search"]):
            with self.subTest(argv=argv):
                self.assertIsNone(parse_args(argv))

    def test_next_quality_cycles(self):
        self.assertEqual([next_quality(q) for q in QUALITIES], [480, 720, 360])

    def test_muxed_audio_needs_no_extra_stream(self):
        self.assertEqual(select_formats({"formats": [self.video("muxed", 360, audio="mp4a.40.2")]}),
                         ("https://media.example/muxed", None))

    def test_opus_when_aac_unavailable(self):
        self.assertEqual(select_formats({"formats": [self.video("v", 480), self.audio("a", "opus")]}),
                         ("https://media.example/v", "https://media.example/a"))

    def test_missing_compatible_streams_fails(self):
        for formats in ([], [self.video("v", 480)], [self.video("v", 1080), self.audio("a")],
                        [self.video("v", 480, "vp9"), self.audio("a")],
                        [{**self.video("v", 480), "url": "file:///tmp/v"}, self.audio("a")]):
            with self.subTest(formats=formats), self.assertRaises(ValueError):
                select_formats({"formats": formats})


class ExtractorTests(unittest.TestCase):
    def test_signed_in_profile_lends_its_cookies(self):
        with tempfile.TemporaryDirectory() as profile:
            self.assertNotIn("cookiesfrombrowser", extractor_options(profile))
            os.mkdir(os.path.join(profile, "Default"))
            open(os.path.join(profile, "Default", "Cookies"), "wb").close()
            self.assertEqual(extractor_options(profile)["cookiesfrombrowser"],
                             ("chromium", profile, "BASICTEXT", None))

    def test_node_is_tried_before_quickjs(self):
        self.assertEqual(list(extractor_options("/nonexistent")["js_runtimes"]), ["node", "quickjs"])

    def test_search_words_are_bounded_and_tidied(self):
        self.assertEqual(validate_query("  chuyện  đêm muộn\ttập 14 "), "chuyện đêm muộn tập 14")
        for value in (None, "", " x ", "a" * 501, "ok\x00no"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_query(value)

    def test_sign_in_refusal_is_told_apart(self):
        self.assertTrue(needs_sign_in(Exception(
            "ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser")))
        self.assertFalse(needs_sign_in(ValueError("No compatible video")))


if __name__ == "__main__":
    unittest.main()
