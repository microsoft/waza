"""Render authored storyboards; capture/claim verification remains a manual gate."""

import argparse
import hashlib
import json
import math
import platform
import shutil
import subprocess
from pathlib import Path
from urllib.parse import urlparse

from PIL import Image, ImageDraw, ImageFont, ImageOps, __version__ as pillow_version

WIDTH, HEIGHT = 1920, 1080
WHITE, MUTED, TEAL, BLUE = "#f3f6fa", "#a6b3c5", "#78e3c2", "#8eb9ff"
PANEL, EDGE = "#121e2c", "#2a3d50"
KINDS = {"hero", "terminal", "code", "image", "cards", "credits", "cta"}


def encoder():
    binary = shutil.which("ffmpeg")
    if binary:
        return binary
    try:
        import imageio_ffmpeg
    except ImportError as error:
        raise RuntimeError("Missing FFmpeg; provide ffmpeg or imageio-ffmpeg.") from error
    return imageio_ffmpeg.get_ffmpeg_exe()


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def load_storyboard(path):
    data = json.loads(path.read_text(encoding="utf-8"))
    for key in ("version", "date", "source_sha", "release_url", "post_url", "repo_url"):
        if not isinstance(data.get(key), str) or not data[key].strip():
            raise ValueError(f"{key} must be a non-empty string")
    source = data["source_sha"]
    if len(source) != 40 or any(c not in "0123456789abcdef" for c in source):
        raise ValueError("source_sha must be the full lowercase 40-character commit SHA")
    for key in ("release_url", "post_url", "repo_url"):
        url = urlparse(data[key])
        if url.scheme != "https" or not url.netloc:
            raise ValueError(f"{key} must be an absolute HTTPS URL")
    scenes = data.get("scenes")
    if not isinstance(scenes, list) or not scenes:
        raise ValueError("scenes must be a non-empty list")
    inputs = {str(path): sha256(path)}
    for scene in scenes:
        if scene.get("kind") not in KINDS:
            raise ValueError(f"Unknown scene kind: {scene.get('kind')}")
        duration = scene.get("duration")
        if (isinstance(duration, bool) or not isinstance(duration, (int, float))
                or not math.isfinite(duration) or duration < 1):
            raise ValueError("Scene duration must be finite and at least one second")
        for key in ("label", "headline", "caption", "provenance"):
            if not isinstance(scene.get(key), str) or not scene[key].strip():
                raise ValueError(f"Every scene needs {key}")
        for key in ("json_file", "image_file"):
            if key in scene:
                asset = (path.parent / scene[key]).resolve(strict=True)
                inputs[str(asset)] = sha256(asset)
                if key == "json_file":
                    payload = json.loads(asset.read_text(encoding="utf-8"))
                    fields = scene.get("fields")
                    if not isinstance(fields, list) or not fields:
                        raise ValueError("Terminal scene needs selected fields")
                    selected = {field: payload[field] for field in fields}
                    scene["_lines"] = json.dumps(selected, indent=2, ensure_ascii=True).splitlines()
                else:
                    with Image.open(asset) as image:
                        image = image.convert("RGB")
                        if "crop" in scene:
                            box = scene["crop"]
                            if (len(box) != 4 or not 0 <= box[0] < box[2] <= image.width
                                    or not 0 <= box[1] < box[3] <= image.height):
                                raise ValueError("Image crop must be inside the actual capture")
                            image = image.crop(tuple(box))
                        scene["_image"] = image.copy()
        if scene["kind"] == "terminal" and "_lines" not in scene:
            raise ValueError("Terminal scene requires captured json_file")
        if scene["kind"] == "image" and "_image" not in scene:
            raise ValueError("Image scene requires captured image_file")
        for kind, field, limit in (("hero", "steps", 3), ("code", "panels", 2),
                                   ("cards", "cards", 3), ("credits", "people", 2)):
            if scene["kind"] == kind:
                values = scene.get(field)
                if not isinstance(values, list) or not 1 <= len(values) <= limit:
                    raise ValueError(f"{kind} requires 1-{limit} {field}")
    return data, inputs


def timestamp(seconds):
    ms = round(seconds * 1000)
    return f"{ms // 3600000:02}:{ms // 60000 % 60:02}:{ms // 1000 % 60:02},{ms % 1000:03}"


class Renderer:
    def __init__(self, data, font_dir):
        self.data = data
        self.duration = sum(scene["duration"] for scene in data["scenes"])
        self.font_dir = font_dir
        self.fonts = {}
        self.base = Image.new("RGB", (WIDTH, HEIGHT), "#0b111b")
        draw = ImageDraw.Draw(self.base)
        for y in range(HEIGHT):
            t = y / HEIGHT
            draw.line((0, y, WIDTH, y), fill=(11 + int(t * 3), 17 + int(t * 7), 27 + int(t * 9)))
        for x in range(1000, 2000, 80):
            draw.line((x, 0, x - 400, HEIGHT), fill="#132030")
        for y in range(40, HEIGHT, 80):
            draw.line((850, y, WIDTH, y), fill="#132030")

    def font(self, size, face="regular"):
        key = size, face
        if key not in self.fonts:
            filename = {"regular": "LiberationSans-Regular.ttf",
                        "bold": "LiberationSans-Bold.ttf",
                        "mono": "LiberationMono-Regular.ttf"}[face]
            self.fonts[key] = ImageFont.truetype(str(self.font_dir / filename), size)
        return self.fonts[key]

    def text(self, draw, position, content, size=34, color=WHITE, face="regular", max_width=None):
        if max_width is not None:
            while size > 16:
                box = draw.multiline_textbbox((0, 0), content, font=self.font(size, face), spacing=12)
                if box[2] - box[0] <= max_width:
                    break
                size -= 1
            if box[2] - box[0] > max_width:
                raise ValueError(f"Text too wide for the template: {content}")
        draw.multiline_text(position, content, font=self.font(size, face), fill=color, spacing=12)

    def panel(self, draw, box, title=None):
        draw.rounded_rectangle(box, radius=22, fill=PANEL, outline=EDGE, width=2)
        if title:
            self.text(draw, (box[0] + 30, box[1] + 24), title, 23, MUTED, "mono",
                      max_width=box[2] - box[0] - 60)
            draw.line((box[0] + 25, box[1] + 75, box[2] - 25, box[1] + 75), fill=EDGE)

    def badge(self, draw, value):
        size = draw.textlength(value, font=self.font(23, "bold")) + 36
        draw.rounded_rectangle((92, 730, 92 + size, 772), radius=9, fill="#192d32")
        self.text(draw, (110, 737), value, 23, TEAL, "bold")

    def split_title(self, draw, scene):
        self.text(draw, (92, 238), scene["label"], 25, TEAL, "bold", max_width=700)
        self.text(draw, (86, 300), scene["headline"], 78, WHITE, "bold", max_width=700)
        self.text(draw, (92, 539), scene.get("body", ""), 33, MUTED, max_width=700)
        if scene.get("badge"):
            self.badge(draw, scene["badge"])
        self.text(draw, (92, 795), scene.get("note", ""), 27, MUTED, max_width=700)

    def scene(self, image, draw, scene, elapsed):
        kind = scene["kind"]
        if kind == "hero":
            self.text(draw, (92, 215), "NEW RELEASE", 25, TEAL, "bold")
            lines = scene["headline"].split("\n")
            for index, line in enumerate(lines):
                self.text(draw, (86, 287 + index * 138), line, 115,
                          WHITE if index == 0 else TEAL, "bold", max_width=1260)
            self.text(draw, (94, 622), scene.get("body", ""), 45, MUTED, max_width=1250)
            self.text(draw, (94, 836), "Agent skill evaluation  /  Open source", 30)
            for index, label in enumerate(scene["steps"]):
                y = 305 + index * 174
                draw.ellipse((1450, y, 1516, y + 66), fill="#193735", outline=TEAL, width=2)
                self.text(draw, (1472, y + 14), str(index + 1), 31, TEAL, "bold")
                self.text(draw, (1542, y + 20), label, 25, WHITE, "bold", max_width=280)
                if index + 1 < len(scene["steps"]):
                    draw.line((1483, y + 80, 1483, y + 157), fill=EDGE, width=3)
        elif kind in ("terminal", "image", "cards"):
            self.split_title(draw, scene)
            if kind == "terminal":
                self.panel(draw, (800, 205, 1830, 910), f"terminal / waza {self.data['version']}")
                command = scene.get("command", "")
                self.text(draw, (840, 311), command[:int(elapsed * 35)], 30, TEAL, "mono", max_width=950)
                lines = scene["_lines"]
                if len(lines) > 16:
                    raise ValueError("Terminal excerpt exceeds 16 lines; select fewer fields")
                line_size = min(29, 410 // max(1, len(lines)))
                visible = max(0, int((elapsed - 1) * 10))
                for index, line in enumerate(lines[:visible]):
                    self.text(draw, (840, 385 + index * (line_size + 8)), line,
                              line_size, BLUE if index % 3 == 2 else WHITE, "mono", max_width=950)
                self.text(draw, (840, 855), scene.get("footer", ""), 23, MUTED, max_width=950)
            elif kind == "image":
                self.panel(draw, (908, 163, 1830, 955), scene["label"])
                capture = ImageOps.contain(scene["_image"], (860, 710), Image.Resampling.LANCZOS)
                image.paste(capture, (1369 - capture.width // 2, 244))
            else:
                for index, card in enumerate(scene["cards"]):
                    y = 250 + index * 192
                    self.panel(draw, (858, y, 1830, y + 154))
                    accent = TEAL if index % 2 == 0 else BLUE
                    draw.rounded_rectangle((858, y + 25, 864, y + 129), radius=3, fill=accent)
                    self.text(draw, (898, y + 31), card["title"], 35, accent, "bold", max_width=890)
                    self.text(draw, (898, y + 92), card["body"], 28, MUTED, max_width=890)
        elif kind in ("code", "credits"):
            self.text(draw, (92, 217), scene["label"], 25, TEAL, "bold")
            self.text(draw, (86, 285), scene["headline"], 76, WHITE, "bold", max_width=1740)
            self.text(draw, (92, 399), scene.get("body", ""), 34, MUTED, max_width=1740)
            entries = scene["panels"] if kind == "code" else scene["people"]
            for index, entry in enumerate(entries):
                x = 92 + index * 888
                if kind == "code":
                    self.panel(draw, (x, 482, x + 846, 885), entry["title"])
                    if len(entry["lines"]) > 8:
                        raise ValueError("Code panel exceeds eight lines")
                    for row, line in enumerate(entry["lines"]):
                        self.text(draw, (x + 30, 588 + row * 37), line, 23,
                                  TEAL if row == len(entry["lines"]) - 1 else WHITE,
                                  "mono", max_width=786)
                else:
                    self.panel(draw, (x, 556, x + 846, 834))
                    self.text(draw, (x + 35, 598), entry["name"], 52, WHITE, "bold", max_width=770)
                    self.text(draw, (x + 35, 668), entry["handle"], 35, TEAL, "mono", max_width=770)
                    self.text(draw, (x + 35, 754), entry["detail"], 28, MUTED, max_width=770)
            self.text(draw, (92, 923), scene.get("footer", ""), 25, MUTED, max_width=1740)
        elif kind == "cta":
            self.text(draw, (92, 215), "AVAILABLE NOW", 25, TEAL, "bold")
            self.text(draw, (86, 284), scene["headline"], 108, WHITE, "bold", max_width=1740)
            self.text(draw, (92, 438), scene.get("body", ""), 61, TEAL, "bold", max_width=1740)
            self.text(draw, (94, 593), self.data["repo_url"], 49, WHITE, "bold", max_width=1740)
            draw.line((94, 695, 1800, 695), fill=EDGE, width=2)
            self.text(draw, (94, 740), "Read the release story", 28, MUTED)
            self.text(draw, (94, 794), self.data["post_url"], 36, WHITE, max_width=1740)
            self.text(draw, (94, 895), self.data["release_url"], 28, MUTED, max_width=1740)

    def frame(self, time):
        start = 0
        for index, scene in enumerate(self.data["scenes"]):
            end = start + scene["duration"]
            if time < end:
                break
            start = end
        elapsed = time - start
        layer = self.base.copy()
        self.scene(layer, ImageDraw.Draw(layer), scene, elapsed)
        fade = max(0, min(1, elapsed / 0.35, (end - time) / 0.2))
        image = Image.blend(self.base, layer, fade)
        draw = ImageDraw.Draw(image)
        draw.rounded_rectangle((86, 63, 135, 112), radius=12, fill="#1c4241")
        draw.line([(96, 88), (104, 88), (110, 75), (117, 101), (123, 86), (128, 86)],
                  fill=TEAL, width=3)
        self.text(draw, (151, 59), "waza", 46, WHITE, "bold")
        self.text(draw, (295, 77), f"/  {self.data['version']}", 25, MUTED, "mono", max_width=550)
        self.text(draw, (1120, 80), scene["label"], 22, TEAL, "bold", max_width=710)
        draw.line((92, 996, 1828, 996), fill=EDGE, width=3)
        draw.line((92, 996, 92 + int(1736 * time / self.duration), 996), fill=TEAL, width=4)
        self.text(draw, (92, 1020), f"{self.data['date']}  /  RELEASE HIGHLIGHTS", 19, MUTED)
        self.text(draw, (1694, 1018), f"{index + 1:02} / {len(self.data['scenes']):02}", 22, MUTED, "mono")
        return image


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("storyboard", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--font-dir", required=True, type=Path)
    parser.add_argument("--fps", type=int, default=24, choices=range(1, 61))
    args = parser.parse_args()
    data, inputs = load_storyboard(args.storyboard.resolve())
    renderer = Renderer(data, args.font_dir)
    output = args.output.resolve()
    poster, captions, manifest = (output.with_suffix(suffix) for suffix in (".png", ".srt", ".json"))
    for path in (output, poster, captions, manifest):
        if path.exists():
            raise FileExistsError(f"Refusing to overwrite {path}")
    binary = encoder()
    version = subprocess.run([binary, "-version"], check=True, capture_output=True, text=True).stdout.splitlines()[0]
    frames = round(renderer.duration * args.fps)
    if not math.isclose(frames / args.fps, renderer.duration):
        raise ValueError("Scene durations must sum to an exact number of frames")
    # Pre-render scene midpoints to reject invalid layout before starting an encoder.
    start = 0
    for scene in data["scenes"]:
        renderer.frame(start + scene["duration"] / 2)
        start += scene["duration"]
    process = subprocess.Popen([
        binary, "-n", "-hide_banner", "-loglevel", "error", "-f", "rawvideo",
        "-pix_fmt", "rgb24", "-s", "1920x1080", "-r", str(args.fps), "-i", "-",
        "-an", "-c:v", "libx264", "-preset", "fast", "-crf", "19",
        "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(output),
    ], stdin=subprocess.PIPE)
    try:
        for number in range(frames):
            process.stdin.write(renderer.frame(number / args.fps).tobytes())
        process.stdin.close()
        if process.wait():
            raise RuntimeError(f"FFmpeg failed; partial output may remain at {output}")
    except BaseException:
        process.kill()
        process.wait()
        raise
    with poster.open("xb") as stream:
        renderer.frame(min(2, data["scenes"][0]["duration"] / 2)).save(stream, format="PNG")
    start, blocks = 0, []
    for index, scene in enumerate(data["scenes"], 1):
        end = start + scene["duration"]
        blocks.append(f"{index}\n{timestamp(start)} --> {timestamp(end)}\n{scene['caption']}\n")
        start = end
    with captions.open("x", encoding="utf-8") as stream:
        stream.write("\n".join(blocks))
    record = {
        "version": data["version"], "source_sha": data["source_sha"],
        "release_url": data["release_url"], "post_url": data["post_url"],
        "inputs_sha256": inputs, "output_sha256": sha256(output),
        "duration_seconds": renderer.duration, "fps": args.fps,
        "resolution": [WIDTH, HEIGHT], "ffmpeg": version, "pillow": pillow_version,
        "python": platform.python_version(), "platform": platform.platform(),
        "renderer_sha256": sha256(Path(__file__)),
        "font_dir": str(args.font_dir),
        "scenes": [{key: value for key, value in scene.items() if not key.startswith("_")}
                   for scene in data["scenes"]],
        "review_status": "unreviewed draft; metadata is not attestation",
    }
    with manifest.open("x", encoding="utf-8") as stream:
        stream.write(json.dumps(record, indent=2) + "\n")
    print(output)


if __name__ == "__main__":
    main()
