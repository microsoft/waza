import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from PIL import Image, ImageStat

from render import Renderer, encoder, load_storyboard, sha256, timestamp

SCRIPTS = Path(__file__).parent
EXAMPLE = SCRIPTS.parent / "assets/storyboard.example.json"
FONTS = Path(os.environ.get("REEL_FONT_DIR", "/usr/share/fonts/truetype/liberation"))


class ReelTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.story = json.loads(EXAMPLE.read_text())
        self.story["source_sha"] = "a" * 40
        self.story["scenes"][1]["json_file"] = "terminal.json"
        self.story["scenes"][3]["image_file"] = "capture.png"
        self.story["scenes"][3]["crop"] = [0, 0, 300, 400]
        # Deliberately synthetic inputs for testing rendering, not product evidence.
        Image.new("RGB", (300, 400), "#815a99").save(self.root / "capture.png")
        (self.root / "terminal.json").write_text(json.dumps({
            "outcome": "success", "exit_code": 0, "engine_calls": 1,
            "selection": {"shard": "shard-a", "task": "selected", "trial": 2, "attempt": 2},
        }))

    def save(self, data=None):
        path = self.root / "storyboard.json"
        path.write_text(json.dumps(data if data is not None else self.story))
        return path

    def run_cli(self, script, *args, success=True):
        result = subprocess.run([sys.executable, str(SCRIPTS / script), *map(str, args)],
                                capture_output=True, text=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def test_requires_real_source_and_assets(self):
        for mutation, message in [
            (lambda d: d.update(source_sha=""), "source_sha"),
            (lambda d: d.update(source_sha="abc"), "source_sha"),
            (lambda d: d.update(post_url="/relative"), "HTTPS"),
            (lambda d: d["scenes"][0].update(duration=True), "duration"),
            (lambda d: d["scenes"][0].update(duration=float("nan")), "duration"),
            (lambda d: d["scenes"][0].update(provenance=""), "provenance"),
            (lambda d: d["scenes"][0].update(kind="fabricated"), "kind"),
            (lambda d: d["scenes"][3].update(crop=[0, 0, 999, 999]), "crop"),
            (lambda d: d["scenes"][1].update(fields=["nonexistent"]), "nonexistent"),
            (lambda d: d["scenes"][5].update(people=[]), "people"),
        ]:
            with self.subTest(message=message):
                story = copy.deepcopy(self.story)
                mutation(story)
                with self.assertRaises((ValueError, KeyError)) as caught:
                    load_storyboard(self.save(story))
                self.assertIn(message, str(caught.exception))
        self.story["scenes"][3]["image_file"] = "missing.png"
        with self.assertRaises(FileNotFoundError):
            load_storyboard(self.save())

    def test_alternative_release_and_observed_failure_are_preserved(self):
        self.story["version"] = "0.41.0"
        (self.root / "terminal.json").write_text(json.dumps({
            "outcome": "evaluation_failure", "exit_code": 1, "engine_calls": 1,
            "selection": {"trial": 1, "attempt": 1},
        }))
        data, inputs = load_storyboard(self.save())
        self.assertEqual(data["version"], "0.41.0")
        self.assertIn('  "outcome": "evaluation_failure",', data["scenes"][1]["_lines"])
        self.assertIn(str(self.root / "capture.png"), inputs)
        self.assertEqual(timestamp(60), "00:01:00,000")
        renderer = Renderer(data, FONTS)
        self.assertEqual(renderer.duration, 60)
        start = 0
        for scene in data["scenes"]:
            frame = renderer.frame(start + scene["duration"] / 2)
            self.assertEqual(frame.size, (1920, 1080))
            self.assertGreater(max(ImageStat.Stat(frame).stddev), 20)
            start += scene["duration"]

    def test_cli_exports_captions_manifest_music_and_no_overwrite(self):
        story = self.save()
        video = self.root / "silent.mp4"
        self.run_cli("render.py", story, "--output", video, "--font-dir", FONTS, "--fps", "1")
        manifest = json.loads(video.with_suffix(".json").read_text())
        self.assertEqual(manifest["duration_seconds"], 60)
        self.assertEqual(manifest["resolution"], [1920, 1080])
        self.assertEqual(manifest["output_sha256"], sha256(video))
        self.assertEqual(manifest["inputs_sha256"][str(story)], sha256(story))
        captions = video.with_suffix(".srt").read_text()
        self.assertIn("00:00:53,000 --> 00:01:00,000", captions)
        for scene in self.story["scenes"]:
            self.assertIn(scene["caption"], captions)
        with Image.open(video.with_suffix(".png")) as poster:
            self.assertEqual(poster.size, (1920, 1080))
        digest = sha256(video)
        failure = self.run_cli("render.py", story, "--output", video,
                               "--font-dir", FONTS, success=False)
        self.assertIn("Refusing to overwrite", failure.stderr)
        self.assertEqual(sha256(video), digest)
        competing = self.root / "competing.png"
        competing.write_bytes(b"existing user content")
        failure = self.run_cli("render.py", story, "--output", competing.with_suffix(".mp4"),
                               "--font-dir", FONTS, success=False)
        self.assertIn("Refusing to overwrite", failure.stderr)
        self.assertEqual(competing.read_bytes(), b"existing user content")
        self.assertFalse(competing.with_suffix(".mp4").exists())
        music = self.root / "electronic.mp4"
        self.run_cli("music.py", video, "--output", music)
        ffmpeg = encoder()
        hashes = []
        for path in (video, music):
            result = subprocess.run([
                ffmpeg, "-v", "error", "-i", str(path), "-map", "0:v:0",
                "-c", "copy", "-f", "streamhash", "-hash", "sha256", "-",
            ], capture_output=True, text=True, check=True)
            hashes.append(result.stdout)
        self.assertEqual(hashes[0], hashes[1], "Music must preserve encoded video bytes")
        decoded = subprocess.run([
            ffmpeg, "-hide_banner", "-i", str(music), "-af", "volumedetect", "-f", "null", "-",
        ], capture_output=True, text=True, check=True)
        self.assertIn("1920x1080", decoded.stderr)
        self.assertIn("00:01:00.00", decoded.stderr)
        self.assertIn("Audio: aac", decoded.stderr)
        self.assertIn("mean_volume:", decoded.stderr)
        self.assertNotIn("mean_volume: -inf", decoded.stderr)
        evidence = json.loads(music.with_suffix(".json").read_text())
        self.assertEqual(evidence["bpm"], 120)
        self.assertEqual(evidence["source_sha256"], digest)
        self.assertEqual(evidence["output_sha256"], sha256(music))
        failure = self.run_cli("music.py", video, "--output", music, success=False)
        self.assertIn("Refusing to overwrite", failure.stderr)
        competing_audio = self.root / "competing.wav"
        competing_audio.write_bytes(b"existing user audio")
        failure = self.run_cli("music.py", video, "--output", competing_audio.with_suffix(".mp4"),
                               success=False)
        self.assertIn("Refusing to overwrite", failure.stderr)
        self.assertEqual(competing_audio.read_bytes(), b"existing user audio")
        short = self.root / "short.mp4"
        subprocess.run([ffmpeg, "-v", "error", "-i", str(video), "-t", "2",
                        "-c", "copy", str(short)], check=True)
        failure = self.run_cli("music.py", short, "--output", self.root / "invalid.mp4",
                               success=False)
        self.assertIn("requires a 60-second source", failure.stderr)
        self.assertFalse((self.root / "invalid.mp4").exists())


if __name__ == "__main__":
    unittest.main()
