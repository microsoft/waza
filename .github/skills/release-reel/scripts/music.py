"""Add the original 120 BPM electronic release-reel soundtrack locally."""

import argparse
import array
import json
import math
import platform
import random
import re
import subprocess
import wave
from pathlib import Path

from render import encoder, sha256

RATE, SECONDS, BPM = 22050, 60, 120
BEAT = 60 / BPM
COUNT = RATE * SECONDS
TAU = math.tau
left = array.array("f")
right = array.array("f")
rng = random.Random(400)


def synth(start, duration, midi, level, pan=0, bass=False):
    frequency = 440 * 2 ** ((midi - 69) / 12)
    begin = int(start * RATE)
    length = max(0, min(int(duration * RATE), COUNT - begin))
    phase = TAU * frequency / RATE
    gl, gr = level * math.sqrt((1 - pan) / 2), level * math.sqrt((1 + pan) / 2)
    for i in range(length):
        t = i / RATE
        envelope = min(1, t / 0.006) * math.exp(-t * (7 if bass else 11))
        envelope *= min(1, max(0, duration - t) / 0.035)
        p = phase * i
        sample = (
            math.sin(p) + 0.32 * math.sin(2 * p)
            + 0.15 * math.sin(3 * p) + 0.07 * math.sin(4 * p)
        ) * envelope
        if not bass:
            sample += 0.12 * math.sin(p * 1.003) * envelope
        left[begin + i] += sample * gl
        right[begin + i] += sample * gr


def drum(start, kind, level):
    duration = {"kick": 0.28, "snare": 0.16, "hat": 0.06}[kind]
    begin = int(start * RATE)
    length = max(0, min(int(duration * RATE), COUNT - begin))
    previous = 0
    for i in range(length):
        t = i / RATE
        noise = rng.uniform(-1, 1)
        high = noise - previous
        previous = noise
        if kind == "kick":
            phase = TAU * (48 * t + 95 * 0.021 * (1 - math.exp(-t / 0.021)))
            sample = math.sin(phase) * math.exp(-t * 18)
        elif kind == "snare":
            sample = (
                0.65 * high * math.exp(-t * 35)
                + 0.3 * math.sin(TAU * 185 * t) * math.exp(-t * 30)
            )
        else:
            sample = high * math.exp(-t * 90)
        sample *= level * min(1, t / 0.001)
        left[begin + i] += sample
        right[begin + i] += sample


def compose():
    global left, right
    left = array.array("f", [0]) * COUNT
    right = array.array("f", [0]) * COUNT
    rng.seed(400)
    chords = [
        (36, [60, 64, 67, 71]),
        (33, [57, 60, 64, 67]),
        (29, [57, 60, 64, 69]),
        (31, [59, 62, 67, 69]),
    ]
    for bar in range(30):
        start = bar * 4 * BEAT
        bass, tones = chords[(bar // 2) % 4]
        breakdown = 14 <= bar < 16
        build = min(1, 0.45 + bar * 0.2)
        for step in range(16):
            when = start + step * BEAT / 4
            if step % 2 == 0 or (bar % 4 == 3 and step > 11):
                pitch = tones[[0, 2, 1, 3, 2, 0, 3, 1][(step // 2) % 8]]
                pitch += 12 if step in (6, 14) else 0
                amplitude = 0.07 * build * (0.7 if breakdown else 1)
                pan = -0.42 if step % 4 == 0 else 0.42
                synth(when, 0.3, pitch, amplitude, pan)
                synth(when + BEAT * 0.75, 0.25, pitch, amplitude * 0.2, -pan)
        for offset in (0, 0.75, 1.5, 2, 2.75, 3.5):
            if not breakdown:
                synth(start + offset * BEAT, 0.3, bass + (12 if offset == 2.75 else 0),
                      0.19 * build, bass=True)
        for beat in range(4):
            if not breakdown and bar > 0:
                drum(start + beat * BEAT, "kick", 0.18)
            if beat % 2 == 1 and bar > 1:
                drum(start + beat * BEAT, "snare", 0.058 if breakdown else 0.085)
            if bar > 0:
                drum(start + beat * BEAT, "hat", 0.014)
                drum(start + (beat + 0.5) * BEAT, "hat", 0.024)
    pcm = array.array("h")
    for i in range(COUNT):
        t = i / RATE
        fade = min(1, t / 0.75, (SECONDS - t) / 2.0)
        for channel in (left, right):
            value = math.tanh(channel[i] * 1.2) * fade
            pcm.append(round(max(-1, min(1, value)) * 32767))
    return pcm


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    source = args.input.resolve(strict=True)
    output = args.output.resolve()
    audio = output.with_suffix(".wav")
    manifest = output.with_suffix(".json")
    for path in (output, audio, manifest):
        if path.exists():
            raise FileExistsError(f"Refusing to overwrite {path}")
    binary = encoder()
    # This soundtrack is deliberately a 60-second format; do not silently truncate other cuts.
    probe = subprocess.run([binary, "-hide_banner", "-i", str(source), "-map", "0:v:0",
                            "-f", "null", "-"], capture_output=True, text=True, check=True)
    match = re.search(r"Duration: (\d+):(\d+):(\d+(?:\.\d+)?)", probe.stderr)
    if not match:
        raise ValueError("Cannot determine source duration")
    hours, minutes, seconds = map(float, match.groups())
    if abs(hours * 3600 + minutes * 60 + seconds - SECONDS) > 0.05:
        raise ValueError("Electronic soundtrack requires a 60-second source")
    with audio.open("xb") as handle:
        with wave.open(handle, "wb") as stream:
            stream.setnchannels(2)
            stream.setsampwidth(2)
            stream.setframerate(RATE)
            stream.writeframes(compose().tobytes())
    subprocess.run([
        binary, "-n", "-hide_banner", "-loglevel", "error",
        "-i", str(source), "-i", str(audio),
        "-map", "0:v:0", "-map", "1:a:0",
        "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-ar", "44100",
        "-af", "loudnorm=I=-18:TP=-2:LRA=7",
        "-t", "60", "-movflags", "+faststart",
        "-metadata", "comment=Original synthesized electronic instrumental, 120 BPM. No sampled or third-party music.",
        str(output),
    ], check=True)
    record = {
        "source_sha256": sha256(source), "output_sha256": sha256(output),
        "soundtrack": "Original synthesized electronic instrumental; no sampled recordings",
        "bpm": BPM, "duration_seconds": SECONDS, "random_seed": 400,
        "python": platform.python_version(), "platform": platform.platform(),
        "synthesizer_sha256": sha256(Path(__file__)),
        "loudness_target_lufs": -18, "true_peak_target_db": -2,
        "ffmpeg": subprocess.run([binary, "-version"], check=True, capture_output=True,
                                 text=True).stdout.splitlines()[0],
        "review_status": "unreviewed; listen before publication",
    }
    with manifest.open("x", encoding="utf-8") as stream:
        stream.write(json.dumps(record, indent=2) + "\n")
    print(output)


if __name__ == "__main__":
    main()
