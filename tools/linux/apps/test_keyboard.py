"""Pure model checks: python3 -m unittest discover -s tools/linux/apps -p test_keyboard.py.

No GTK/display required. Deployment owner runs these; they do not test hardware.
"""

import unittest
import unicodedata

from keyboard import apply_tone, delete_grapheme


class ToneTests(unittest.TestCase):
    def test_all_tones_preserve_vowel_shape(self):
        for mark, expected in (("\u0301", "ắ"), ("\u0300", "ằ"),
                               ("\u0309", "ẳ"), ("\u0303", "ẵ"), ("\u0323", "ặ")):
            with self.subTest(mark=mark):
                self.assertEqual(apply_tone("ă", mark), expected)

    def test_replace_and_remove_tone_without_removing_shape(self):
        self.assertEqual(apply_tone("tiếng", "\u0300"), "tiềng")
        self.assertEqual(apply_tone("tiếng", ""), "tiêng")
        self.assertEqual(apply_tone("ƯỚC", "\u0309"), "ƯỞC")

    def test_explicit_multivowel_composition(self):
        text = apply_tone("tiê", "\u0301") + "ng Việt"
        self.assertEqual(text, "tiếng Việt")
        self.assertEqual(apply_tone("ho", "\u0300") + "a", "hòa")

    def test_word_boundary_does_not_rewrite_earlier_word(self):
        for text in ("xin ", "xin.", "xin ch", "https://", "đ"):
            with self.subTest(text=text):
                self.assertEqual(apply_tone(text, "\u0301"), text)

    def test_combining_input_normalizes_to_nfc(self):
        result = apply_tone("a\u0306\u0300", "\u0301")
        self.assertEqual(result, "ắ")
        self.assertTrue(unicodedata.is_normalized("NFC", result))

    def test_invalid_tone_rejected(self):
        with self.assertRaises(ValueError):
            apply_tone("a", "; command")


class GraphemeTests(unittest.TestCase):
    def test_vietnamese_nfc_and_nfd(self):
        self.assertEqual(delete_grapheme("Việt"), "Việ")
        self.assertEqual(delete_grapheme("xắ"), "x")
        self.assertEqual(delete_grapheme("xa\u0306\u0301"), "x")
        self.assertEqual(delete_grapheme("xA\u0302\u0323"), "x")

    def test_empty_and_ascii(self):
        self.assertEqual(delete_grapheme(""), "")
        self.assertEqual(delete_grapheme("a"), "")
        self.assertEqual(delete_grapheme("ab "), "ab")

    def test_common_emoji_clusters(self):
        self.assertEqual(delete_grapheme("x\U0001f44d\U0001f3fd"), "x")
        self.assertEqual(delete_grapheme("x\U0001f469\u200d\U0001f4bb"), "x")
        self.assertEqual(delete_grapheme("x\U0001f1fb\U0001f1f3"), "x")
        self.assertEqual(delete_grapheme("\U0001f1fb\U0001f1f3\U0001f1fa\U0001f1f8"),
                         "\U0001f1fb\U0001f1f3")


if __name__ == "__main__":
    unittest.main()
