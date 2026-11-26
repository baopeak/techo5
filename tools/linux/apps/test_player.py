"""Pure player selection/URL tests; no network, GTK or subprocesses.

Run by deployment owner: python3 -m unittest discover -s tools/linux/apps.
"""
import os
import tempfile
import time
import unittest

from player import (QUALITIES, audio_choices, best_result, compact_vtt, extractor_options,
                    fetch_subtitles, needs_sign_in, next_quality, parse_args, plain_words,
                    quality_from, select_formats, subtitle_sources, validate_query, validate_url)


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

    def test_original_audio_beats_auto_dubs_at_a_higher_bitrate(self):
        # As YouTube lists a video with auto-dubs (0e3GPea1Tyg on crown, 2026-10-24).
        dubs = [{**self.audio("140-%d" % n, bitrate=129.478), "language": lang, "language_preference": -1,
                 "format_note": name + ", medium"}
                for n, (lang, name) in enumerate([("ml", "Malayalam"), ("vi", "Vietnamese"), ("tr", "Turkish")])]
        original = {**self.audio("140-23", bitrate=129.475), "language": "en-US", "language_preference": 10,
                    "format_note": "English (US) original (default), medium"}
        formats = [self.video("720", 720)] + dubs + [original, self.audio("opus-orig", "opus", 160)]
        self.assertEqual(select_formats({"formats": formats})[1], "https://media.example/140-23")

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


class TrackTests(unittest.TestCase):
    def audio(self, fid, lang, pref=-1, note="medium", codec="mp4a.40.2", abr=129.4):
        return {"format_id": fid, "url": "https://media.example/" + fid, "vcodec": "none", "acodec": codec,
                "abr": abr, "language": lang, "language_preference": pref, "format_note": note}

    def test_dubs_offered_beside_the_original(self):
        original = self.audio("140-9", "en-US", 10, "English (US) original (default), medium")
        info = {"formats": [self.audio("140-1", "ml"), self.audio("140-2", "vi"), self.audio("140-3", "en-GB"),
                            self.audio("251-2", "vi", codec="opus"), original]}
        self.assertEqual(audio_choices(info, original), [
            {"label": "Âm thanh gốc", "url": "https://media.example/140-9"},
            {"label": "Lồng tiếng Việt", "url": "https://media.example/140-2"},
            {"label": "Lồng tiếng Anh", "url": "https://media.example/140-3"}])
        # No dub, or a video carrying its own sound: no choice to offer.
        self.assertEqual(audio_choices({"formats": [original]}, original), [])
        self.assertEqual(audio_choices(info, None), [])

    def test_subtitles_prefer_the_uploaders_then_automatic(self):
        vtt = lambda name: [{"ext": "json3", "url": "https://x/" + name + ".json"},
                            {"ext": "vtt", "url": "https://x/" + name + ".vtt"}]
        info = {"subtitles": {"en-GB": vtt("en-GB"), "live_chat": vtt("chat")},
                "automatic_captions": {"vi": vtt("vi-tr"), "vi-orig": vtt("vi-orig"), "en": vtt("en-tr")}}
        self.assertEqual(subtitle_sources(info), [("Tiếng Việt (tự động)", "https://x/vi-orig.vtt"),
                                                  ("English", "https://x/en-GB.vtt")])
        self.assertEqual(subtitle_sources({"automatic_captions": {"vi": vtt("vi")}}),
                         [("Tiếng Việt (dịch tự động)", "https://x/vi.vtt")])
        self.assertEqual(subtitle_sources({}), [])

    def test_rolling_auto_captions_compact_to_plain_lines(self):
        raw = ("WEBVTT\nKind: captions\nLanguage: vi\n\n"
               "00:00:00.000 --> 00:00:02.000 align:start position:0%\n"
               "Tôi<00:00:00.350><c> đã</c><c> về</c>\n\n"
               "00:00:02.000 --> 00:00:02.010 align:start position:0%\nTôi đã về\n\n"
               "00:00:02.010 --> 00:00:04.000 align:start position:0%\nTôi đã về\nnhà &amp; ngủ\n")
        self.assertEqual(compact_vtt(raw), "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nTôi đã về\n\n"
                                           "00:00:02.010 --> 00:00:04.000\nnhà & ngủ\n")

    def test_slow_subtitles_do_not_hold_up_the_video(self):
        class Response:
            def __init__(self, body):
                self.body = body

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def read(self, limit):
                return self.body

        class Downloader:
            def urlopen(self, url):
                if "slow" in url:
                    time.sleep(2)
                if "bad" in url:
                    raise OSError("429")
                return Response(b"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nxin ch\xc3\xa0o\n")

        start = time.monotonic()
        got = fetch_subtitles(Downloader(), [("A", "https://x/fast"), ("B", "https://x/slow"),
                                             ("C", "https://x/bad")], wait=0.5)
        self.assertLess(time.monotonic() - start, 1.5)
        self.assertEqual([s["label"] for s in got], ["A"])
        self.assertIn("xin chào", got[0]["vtt"])


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

    # YouTube's first results on crown for the title heard with wrong marks, then searched without
    # marks (2026-10-23); the episode asked for is only in the second list, third.
    HEARD = "chuyến đêm muôn tập 14"
    RESULTS = [
        ("NPUaSVjCrg4", "2 Ngày 1 Đêm Việt Nam|Tập 14: Dương Lâm hóa bác sĩ mổ ruột thừa"),
        ("NMBOiIenTyI", "Vua Lính Đánh Thuê Mạnh Nhất | HH VietSub"),
        ("CtRwKf49SSQ", "Ơn Giời Cậu Đây Rồi Mùa 8 Tập 14 | Khánh Phương hóa trai Nhật"),
        ("eY8fmh4EnqQ", "ĐÊM ĐỊNH MỆNH: BẾN ĐỖ MUỘN MÀNG - [TẬP 14] - TRUYỆN TÂM LÝ XÃ HỘI HAY"),
        ("_ZZvBSZb4dA", "TÌNH MUỘN - Tập 14 | Mạnh Tử Nghĩa & Trương Lăng Hách"),
        ("8CFIqnX52WY", "CHUYỆN ĐÊM MUỘN #14: KHI BI QUAN VÀ BUỒN CHÁN NHẤT TÔI LÀM GÌ"),
    ]

    def test_title_heard_with_wrong_marks_finds_the_episode(self):
        entries = [{"id": i, "title": t} for i, t in self.RESULTS]
        self.assertEqual(plain_words("Chuyện ĐÊM muộn #14"), ["chuyen", "dem", "muon", "14"])
        self.assertEqual(best_result(self.HEARD, entries)["id"], "8CFIqnX52WY")
        self.assertEqual(best_result("chuyện đêm muộn tập 14", entries)["id"], "8CFIqnX52WY")

    def test_no_matching_title_keeps_youtubes_first(self):
        entries = [{"id": "NMBOiIenTyI", "title": "Vua Lính"}, {"id": "abcdefgh_-1", "title": "Khác"}]
        self.assertEqual(best_result("zzz qqq", entries)["id"], "NMBOiIenTyI")
        with self.assertRaises(ValueError):
            best_result("x y", [{"id": "not an id", "title": "x y"}])

    def test_sign_in_refusal_is_told_apart(self):
        self.assertTrue(needs_sign_in(Exception(
            "ERROR: [youtube] abc: Sign in to confirm you’re not a bot. Use --cookies-from-browser")))
        self.assertFalse(needs_sign_in(ValueError("No compatible video")))


if __name__ == "__main__":
    unittest.main()
