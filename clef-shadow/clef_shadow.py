#!/usr/bin/env python3
"""clef-shadow: parallel tank-activity detector using Cloudflare Clef. Runs alongside sanddune,
never touches it.

Loop inside the scheduled window:
  IDLE    every gate.interval_s:   1 frame  -> Clef-Flash  "is a person present?"
  ACTIVE  every active.interval_s: frame + tank-top crop -> Clef-omni, 9 observable questions
          back to IDLE after active.leave_after_s with no person in any frame
  DONE    >= decision.frames_needed frames in ONE visit with hands at the tank opening AND a
          hose or rod held -> Telegram (tagged) -> stop for the day
  DEADLINE reached without DONE -> Telegram "no tank activity seen" -> stop for the day

Standard library + ffmpeg only (macOS /usr/bin/python3 3.9 is enough).

  python3 clef_shadow.py run                     # the service (launchd runs this)
  python3 clef_shadow.py selftest [--notify]     # one real camera grab + both model calls
  python3 clef_shadow.py replay FILE HH:MM:SS     # run the same logic over a recorded clip
"""
import base64, datetime as dt, json, os, shutil, subprocess, sys, tempfile, time, traceback
import urllib.error, urllib.parse, urllib.request
from typing import Optional

HERE = os.path.dirname(os.path.abspath(__file__))
DAYS = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"]
B64_BUDGET = 230_000  # Workers AI pre-checks base64 length against the context window

GATE_Q = {"person_present": {"type": "noul", "instructions": "Is at least one person visible anywhere in the image?"}}
STATE_TEXT = ("Two images from a fixed CCTV camera in a utility room containing several large tanks. "
              "Image 1 is the full frame. Image 2 is a zoomed-in crop of the area at the top of the "
              "tanks, taken from the same frame.")
Q = lambda s: {"type": "noul", "instructions": s}
ACTIVE_Q = {
    "person_count": {"type": "score", "instructions": "How many people are visible in the full frame?",
                     "criteria": ["No person visible", "One person", "Two people", "Three or more people"]},
    "person_on_top_of_tank": Q("Is any person standing, kneeling or climbing on top of or onto a tank?"),
    "hands_at_tank_opening": Q("Are any person's hands at the top, lid or opening of a tank?"),
    "hose_or_pipe_held_by_person": Q("Is any person holding or handling a flexible hose or pipe (any colour)?"),
    "rod_held_by_person": Q("Is any person holding a long thin rigid rod or stick?"),
    "hose_or_pipe_into_tank_opening": Q("Does a hose or pipe enter an opening on top of a tank, whether or not anyone is holding it?"),
    "rod_into_tank_opening": Q("Is a rod or stick inside or entering an opening on top of a tank?"),
    "tank_lid_open": Q("Is any tank lid or hatch open?"),
    "visibility": {"type": "choice", "instructions": "How clearly can the people and objects be seen?",
                   "criteria": {"clear": "People and objects are clearly visible",
                                "partly_hidden": "A person or object is partly hidden or at the edge of the frame",
                                "dark_or_night": "Image is dark or black-and-white night mode",
                                "nothing_to_judge": "No people or objects of interest"}},
}


# ---------------------------------------------------------------- config & logging
def load_env(path):
    if os.path.exists(path):
        for line in open(path):
            k, sep, v = line.strip().partition("=")
            if sep and not k.startswith("#"):
                os.environ.setdefault(k.strip(), v.strip().strip("'\""))


def load_config():
    cfg = json.load(open(os.path.join(HERE, "config.json")))
    load_env(os.path.join(HERE, ".env"))
    cfg["_account"] = os.environ.get("CLOUDFLARE_ACCOUNT_ID", "")
    cfg["_token"] = os.environ.get("CLOUDFLARE_AUTH_TOKEN", "") or os.environ.get("CLOUDFLARE_API_TOKEN", "")
    return cfg


def log(msg):
    line = f"{dt.datetime.now():%Y-%m-%d %H:%M:%S} {msg}"
    print(line, flush=True)


def ffmpeg_bin(cfg):
    if cfg.get("ffmpeg_path"):
        return cfg["ffmpeg_path"]
    extra = "/opt/homebrew/bin:/usr/local/bin:/usr/bin"  # launchd starts with a minimal PATH
    return shutil.which("ffmpeg", path=os.environ.get("PATH", "") + ":" + extra) or "ffmpeg"


# ---------------------------------------------------------------- camera
def grab(cfg, src, out_dir, offset_s=None, with_crop=True):
    """One frame -> (full.jpg, crop.jpg|None). Fixes anamorphic pixels, crops the tank tops."""
    scale = cfg.get("aspect_fix_width_scale")
    fix = f"scale=iw*{scale}:ih,setsar=1" if scale else "scale=iw*sar:ih,setsar=1"
    full, crop = os.path.join(out_dir, "full.jpg"), os.path.join(out_dir, "crop.jpg")
    cmd = [ffmpeg_bin(cfg), "-v", "error", "-y"]
    if src.startswith("rtsp"):
        cmd += ["-rtsp_transport", "tcp"]
    if offset_s is not None:
        cmd += ["-ss", f"{offset_s:.2f}"]
    cmd += ["-i", src]
    w = cfg.get("send_width", 1024)
    c = cfg.get("tank_top_crop_fraction")  # [x, y, w, h] as fractions of the corrected frame
    if with_crop and c:
        cmd += ["-filter_complex",
                f"[0:v]{fix},split=2[a][b];[a]scale={w}:-2[f];[b]crop=iw*{c[2]}:ih*{c[3]}:iw*{c[0]}:ih*{c[1]}[c]",
                "-map", "[f]", "-frames:v", "1", "-q:v", "5", full,
                "-map", "[c]", "-frames:v", "1", "-q:v", "4", crop]
    else:
        cmd += ["-vf", f"{fix},scale={w}:-2", "-frames:v", "1", "-q:v", "5", full]
        crop = None
    subprocess.run(cmd, check=True, timeout=cfg.get("grab_timeout_s", 25),
                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    return full, crop


# ---------------------------------------------------------------- Clef (Workers AI)
def clef(cfg, model, state, questions, images):
    imgs = [{"content_type": "image/jpeg", "base64": base64.b64encode(open(p, "rb").read()).decode()} for p in images]
    if sum(len(i["base64"]) for i in imgs) > B64_BUDGET:
        raise RuntimeError("images too large for the request budget; lower send_width")
    url = f"https://api.cloudflare.com/client/v4/accounts/{cfg['_account']}/ai/run/@cf/cloudflare/{model}"
    body = json.dumps({"model": model, "state": state, "questions": questions, "images": imgs}).encode()
    last = None
    for attempt in range(3):
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Authorization": f"Bearer {cfg['_token']}", "Content-Type": "application/json"})
        try:
            t = time.perf_counter()
            with urllib.request.urlopen(req, timeout=60) as r:
                p = json.load(r)
            res = p.get("result", p)
            res["_ms"] = round((time.perf_counter() - t) * 1000)
            return res
        except urllib.error.HTTPError as e:
            last = f"HTTP {e.code}: {e.read().decode(errors='replace')[:300]}"
            if e.code < 500 and e.code != 429:
                break
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            last = str(e)
        time.sleep(2 ** attempt)
    raise RuntimeError(f"Clef {model} failed: {last}")


def simple(res):
    out = {}
    for q, a in res.get("answers", {}).items():
        out[q] = a["noul"] if a["type"] == "noul" else (a["score"] if a["type"] == "score" else a["choice"])
    return out


# ---------------------------------------------------------------- Telegram
def telegram(cfg, text, photo=None, dry=False):
    tg = cfg.get("telegram", {})
    text = f"{cfg.get('message_prefix', '[Clef test]')} {text}"
    if dry or not tg.get("bot_token"):
        log(f"TELEGRAM (not sent): {text}")
        return
    for chat in tg.get("chat_ids", []):
        try:
            if photo:
                boundary = "----clefshadow"
                parts = [("chat_id", chat), ("caption", text[:1000])]
                body = b"".join(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode()
                                for k, v in parts)
                body += (f'--{boundary}\r\nContent-Disposition: form-data; name="photo"; filename="frame.jpg"\r\n'
                         f"Content-Type: image/jpeg\r\n\r\n").encode() + open(photo, "rb").read() + f"\r\n--{boundary}--\r\n".encode()
                req = urllib.request.Request(f"https://api.telegram.org/bot{tg['bot_token']}/sendPhoto", data=body,
                                             headers={"Content-Type": f"multipart/form-data; boundary={boundary}"})
            else:
                req = urllib.request.Request(f"https://api.telegram.org/bot{tg['bot_token']}/sendMessage",
                                             data=urllib.parse.urlencode({"chat_id": chat, "text": text}).encode())
            urllib.request.urlopen(req, timeout=30).read()
            log(f"telegram sent to {chat}")
        except Exception as e:  # never let a notification failure kill the loop
            log(f"telegram FAILED for {chat}: {e}")


# ---------------------------------------------------------------- the decision logic (shared by run + replay)
class Detector:
    """One day's state machine. step() takes a timestamp + source, returns seconds to wait."""

    def __init__(self, cfg, rec_dir):
        self.cfg, self.rec_dir = cfg, rec_dir
        self.mode, self.last_seen, self.visit, self.done, self.result = "idle", None, [], False, None
        os.makedirs(rec_dir, exist_ok=True)
        self.calls = open(os.path.join(rec_dir, "calls.jsonl"), "a")

    def _record(self, now, phase, res, full, crop, keep):
        stamp = now.strftime("%H%M%S")
        saved = None
        if keep:
            saved = os.path.join(self.rec_dir, f"{stamp}_{phase}.jpg")
            shutil.copy(full, saved)
            if crop:
                shutil.copy(crop, os.path.join(self.rec_dir, f"{stamp}_{phase}_crop.jpg"))
        self.calls.write(json.dumps({"time": now.isoformat(timespec="seconds"), "phase": phase, "ms": res.get("_ms"),
                                     "tokens": res.get("usage", {}).get("input_tokens"), "answers": simple(res),
                                     "frame": saved}) + "\n")
        self.calls.flush()
        return saved

    def step(self, now, src, offset_s=None):
        g, a, d = self.cfg["gate"], self.cfg["active"], self.cfg["decision"]
        with tempfile.TemporaryDirectory() as tmp:
            if self.mode == "idle":
                full, crop = grab(self.cfg, src, tmp, offset_s, with_crop=False)
                res = clef(self.cfg, g["model"], "A frame from a fixed CCTV camera in a utility room.", GATE_Q, [full])
                p = simple(res)["person_present"]
                self._record(now, "gate", res, full, None, keep=p >= g["threshold"] or now.minute % 5 == 0 and now.second < g["interval_s"])
                log(f"gate  person={p:.2f}")
                if p < g["threshold"]:
                    return g["interval_s"]
                self.mode, self.last_seen, self.visit = "active", now, []
                log("-> ACTIVE (person present)")
            full, crop = grab(self.cfg, src, tmp, offset_s, with_crop=True)
            res = clef(self.cfg, a["model"], STATE_TEXT, ACTIVE_Q, [full, crop] if crop else [full])
            s = simple(res)
            saved = self._record(now, "active", res, full, crop, keep=True)
        hose, rod, hands = s["hose_or_pipe_held_by_person"], s["rod_held_by_person"], s["hands_at_tank_opening"]
        qualifies = hands >= d["hands_min"] and max(hose, rod) >= d["object_min"]
        log(f"active persons~{s['person_count']:.1f} hands={hands:.2f} hose={hose:.2f} rod={rod:.2f}"
            f"{'  <- qualifies' if qualifies else ''}")
        if s["person_count"] >= 0.5:
            self.last_seen = now
        if qualifies:
            self.visit.append({"time": now, "hose": hose, "rod": rod, "hands": hands, "frame": saved})
        if len(self.visit) >= d["frames_needed"]:
            margin = sum(v["rod"] - v["hose"] for v in self.visit) / len(self.visit)
            kind = "MEASUREMENT (rod)" if margin > 0 else "REFILL (hose)"
            best = max(self.visit, key=lambda v: v["hands"] + max(v["hose"], v["rod"]))
            self.done = True
            self.result = (f"Tank activity seen at {self.visit[0]['time']:%H:%M}: looks like {kind}. "
                           f"{len(self.visit)} frames with hands at the tank opening holding a hose/rod "
                           f"(rod-vs-hose {margin:+.2f}; positive = rod). Parallel test detector, not the official alarm.")
            return 0
        if (now - self.last_seen).total_seconds() >= a["leave_after_s"]:
            self.mode, self.visit = "idle", []
            log("-> IDLE (no person for %ss)" % a["leave_after_s"])
            return g["interval_s"]
        return a["interval_s"]


# ---------------------------------------------------------------- commands
def day_state_path(day):
    os.makedirs(os.path.join(HERE, "state"), exist_ok=True)
    return os.path.join(HERE, "state", f"{day}.json")


def cmd_run():
    log("clef-shadow starting")
    det, det_day, errors = None, None, 0
    while True:
        cfg = load_config()  # re-read every cycle, like sanddune
        sch, now = cfg["schedule"], dt.datetime.now()
        today = now.date().isoformat()
        in_day = DAYS[now.weekday()] == sch["day"].lower()
        if not in_day or now.hour < sch["window_start_hour"]:
            time.sleep(60); continue
        sp = day_state_path(today)
        st = json.load(open(sp)) if os.path.exists(sp) else {}
        if st.get("finished"):
            time.sleep(60); continue
        if now.hour >= sch["deadline_hour"]:
            telegram(cfg, f"No tank activity seen between {sch['window_start_hour']:02d}:00 and "
                          f"{sch['deadline_hour']:02d}:00. Parallel test detector, not the official alarm.")
            json.dump({**st, "finished": True, "result": "none"}, open(sp, "w")); continue
        if not st.get("started"):
            telegram(cfg, f"Watching the tanks until {sch['deadline_hour']:02d}:00 (parallel test, no action needed).")
            st["started"] = True; json.dump(st, open(sp, "w"))
        if det is None or det_day != today:
            det, det_day = Detector(cfg, os.path.join(HERE, "records", today)), today
        det.cfg = cfg
        t0 = time.time()
        try:
            wait = det.step(now, cfg["rtsp_url"])
            errors = 0
        except Exception as e:
            errors += 1
            log(f"ERROR ({errors} in a row): {e}")
            if errors == cfg.get("error_alert_after", 10):
                telegram(cfg, f"Problem: {errors} failures in a row, latest: {str(e)[:200]}")
            wait = 30
        if det.done:
            best = max(det.visit, key=lambda v: v["hands"])["frame"]
            telegram(cfg, det.result, photo=best)
            json.dump({**st, "finished": True, "result": det.result}, open(sp, "w"))
            continue
        time.sleep(max(0, wait - (time.time() - t0)))


def cmd_replay(path, start):
    """Same Detector over a recorded clip on a virtual clock. Real Clef calls, Telegram printed only."""
    cfg = load_config()
    dur = float(subprocess.run([ffmpeg_bin(cfg).replace("ffmpeg", "ffprobe"), "-v", "error", "-show_entries",
                                "format=duration", "-of", "csv=p=0", path], capture_output=True, text=True).stdout)
    t0 = dt.datetime.combine(dt.date.today(), dt.datetime.strptime(start, "%H:%M:%S").time())
    name = os.path.splitext(os.path.basename(path))[0]
    det = Detector(cfg, os.path.join(HERE, "records", f"replay_{name}"))
    off, calls = 0.0, 0
    while off < dur - 0.5 and not det.done:
        now = t0 + dt.timedelta(seconds=off)
        print(f"[{now:%H:%M:%S}] ", end="")
        wait = det.step(now, path, offset_s=off)
        calls += 1
        off += wait
    if det.done:
        telegram(cfg, det.result, dry=True)
    else:
        log(f"end of clip, no activity confirmed ({calls} steps)")


def cmd_selftest(notify):
    cfg = load_config()
    ok = True
    for k in ["rtsp_url", "_account", "_token"]:
        if not cfg.get(k):
            print(f"FAIL config: {k} missing"); ok = False
    with tempfile.TemporaryDirectory() as tmp:
        try:
            full, crop = grab(cfg, cfg["rtsp_url"], tmp)
            keep = os.path.join(HERE, "selftest_frame.jpg"); shutil.copy(full, keep)
            if crop: shutil.copy(crop, os.path.join(HERE, "selftest_crop.jpg"))
            print(f"OK   camera: frame saved to {keep} (check the crop in selftest_crop.jpg covers the tank tops)")
            r = clef(cfg, cfg["gate"]["model"], "A frame from a fixed CCTV camera in a utility room.", GATE_Q, [full])
            print(f"OK   gate ({cfg['gate']['model']}): person_present={simple(r)['person_present']:.2f}, {r['_ms']} ms")
            r = clef(cfg, cfg["active"]["model"], STATE_TEXT, ACTIVE_Q, [full, crop] if crop else [full])
            print(f"OK   active ({cfg['active']['model']}): {r['_ms']} ms, {r.get('usage', {}).get('input_tokens')} tokens")
        except Exception as e:
            print(f"FAIL {e}"); ok = False
    if notify:
        telegram(cfg, "Self-test from the parallel tank detector (no action needed).")
        print("sent Telegram test message")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    args = sys.argv[1:]
    if not args or args[0] == "run":
        cmd_run()
    elif args[0] == "replay":
        cmd_replay(args[1], args[2])
    elif args[0] == "selftest":
        cmd_selftest("--notify" in args)
    else:
        print(__doc__); sys.exit(2)
