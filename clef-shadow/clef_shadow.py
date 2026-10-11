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
  python3 clef_shadow.py replay FILE HH:MM:SS [--full]  # same logic over a recorded clip
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
    # defaults for keys an older config.json may lack (e.g. one filled before Layer 2 existed)
    cfg.setdefault("layer2", {"model": "clef", "threshold": 0.8, "max_rows": 400})
    cfg.setdefault("decision", {"hands_min": 0.6, "object_min": 0.6, "frames_needed": 3})
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
def stream(cfg, layer):
    """(rtsp_url, aspect_fix_width_scale) for a layer ("gate" or "active"). Each layer may name its own stream:
    the gate can use a low-res sub-stream that is NOT anamorphic, the active layer the anamorphic main stream,
    so the URL and the aspect fix are configured together. Falls back to the top-level keys."""
    lc = cfg.get(layer, {})
    url = lc.get("rtsp_url") or cfg.get("rtsp_url", "")
    aspect = lc["aspect_fix_width_scale"] if "aspect_fix_width_scale" in lc else cfg.get("aspect_fix_width_scale")
    return url, aspect


def grab(cfg, layer, out_dir, offset_s=None, with_crop=True, src=None):
    """One frame -> (full.jpg, crop.jpg|None). Fixes anamorphic pixels, never upscales, crops the tank tops.
    src overrides the layer's stream (replay of a recorded file). One retry: this DVR throws transient
    '453 Not Enough Bandwidth' when several clients pull at once."""
    url, scale = stream(cfg, layer)
    src = src or url
    fix = f"scale=iw*{scale}:ih,setsar=1" if scale else "scale=iw*sar:ih,setsar=1"
    full, crop = os.path.join(out_dir, "full.jpg"), os.path.join(out_dir, "crop.jpg")
    cmd = [ffmpeg_bin(cfg), "-v", "error", "-y"]
    if src.startswith("rtsp"):
        cmd += ["-rtsp_transport", "tcp"]
    if offset_s is not None:
        cmd += ["-ss", f"{offset_s:.2f}"]
    cmd += ["-i", src]
    down = f"scale=w=min(iw\\,{cfg.get('send_width', 1024)}):h=-2"  # cap the width, never upscale
    c = cfg.get("tank_top_crop_fraction")  # [x, y, w, h] as fractions of the corrected frame
    if with_crop and c:
        cmd += ["-filter_complex",
                f"[0:v]{fix},split=2[a][b];[a]{down}[f];[b]crop=iw*{c[2]}:ih*{c[3]}:iw*{c[0]}:ih*{c[1]}[c]",
                "-map", "[f]", "-frames:v", "1", "-q:v", "5", full,
                "-map", "[c]", "-frames:v", "1", "-q:v", "4", crop]
    else:
        cmd += ["-vf", f"{fix},{down}", "-frames:v", "1", "-q:v", "5", full]
        crop = None
    err = ""
    for attempt in range(2):
        try:
            subprocess.run(cmd, check=True, timeout=cfg.get("grab_timeout_s", 25),
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            return full, crop
        except subprocess.CalledProcessError as e:
            err = e.stderr.decode(errors="replace").strip()[-200:]
        except subprocess.TimeoutExpired:
            err = "timeout"
        time.sleep(2)
    raise RuntimeError(f"camera grab failed ({layer} stream): {err}")


# ---------------------------------------------------------------- Clef (Workers AI)
def clef(cfg, model, state, questions, images):
    imgs = [{"content_type": "image/jpeg", "base64": base64.b64encode(open(p, "rb").read()).decode()} for p in images]
    if sum(len(i["base64"]) for i in imgs) > B64_BUDGET:
        raise RuntimeError("images too large for the request budget; lower send_width")
    url = f"https://api.cloudflare.com/client/v4/accounts/{cfg['_account']}/ai/run/@cf/cloudflare/{model}"
    body = json.dumps({"model": model, "state": state, "questions": questions, **({"images": imgs} if imgs else {})}).encode()
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


# ---------------------------------------------------------------- Layer 2 (Clef reads the day's timeline)
L2_STATE = """You interpret observations from a fixed CCTV camera in a utility room with several large water tanks.
A vision model (Layer 1) looked at one frame every 5 seconds while a person was present and answered
yes/no questions about what is VISIBLE; each number is its probability (0-1) that the answer is yes.
"persons" is the expected number of people. Lines starting with "--- visit" mark a new visit (gap > 30 s).

Site knowledge:
- MEASUREMENT: a person climbs to the tank tops and dips a long rod into a tank opening to check the level,
  often several times or at several tanks, then leaves. Typically 1-3 minutes.
- REFILL: a person brings a hose to a tank, puts it into the tank opening, and the hose stays there for
  minutes while water flows; people may come and go while the hose stays in.
- NONE: people walk through, look around, or work elsewhere without working at a tank opening.

Known quirks of the Layer 1 numbers:
- Single frames are noisy. Base the decision on patterns sustained over several frames, never one frame.
- Rod and hose are often confused: in rod frames hose_held can also be high. Compare rod_held vs hose_held
  across the visit instead of reading either alone; a hose staying in the opening for minutes means refill.
- hands@open briefly high while someone passes the tanks is NOT activity; sustained hands@open with an
  object held is.
- The "into opening" questions are weak; treat them as supporting evidence only.
- persons can drop to ~0 for a few frames while the person is still there (hidden behind tanks).
"""
L2_Q = {"activity": {"type": "choice",
        "instructions": "Based on the whole sequence of observations, what tank activity happened today?",
        "criteria": {
            "measurement": "A person dips a long rod into a tank opening to check the level, often several times: "
                           "rod_held stays above hose_held over several frames with hands at the opening.",
            "refill": "A hose is put into a tank opening and stays there for minutes: hose_held stays above rod_held "
                      "and hands@open / hose_into stay elevated over many frames.",
            "none": "People walk past or work elsewhere: no sustained hands at a tank opening with a hose or rod held."}}}
L2_COLS = [("person_count", "persons"), ("hands_at_tank_opening", "hands@open"), ("person_on_top_of_tank", "on_tank"),
           ("hose_or_pipe_held_by_person", "hose_held"), ("rod_held_by_person", "rod_held"),
           ("hose_or_pipe_into_tank_opening", "hose_into"), ("rod_into_tank_opening", "rod_into"),
           ("tank_lid_open", "lid_open"), ("visibility", "visibility")]


def l2_table(rows, max_rows):
    rows = rows[-max_rows:]
    lines, last = ["time     " + " ".join(f"{h:>10}" for _, h in L2_COLS)], None
    for r in rows:
        t = dt.datetime.fromisoformat(r["time"])
        if last is None or (t - last).total_seconds() > 30:
            lines.append(f"--- visit starting {t:%H:%M:%S}")
        last = t
        a = r["answers"]
        lines.append(r["time"][11:19] + " " + " ".join(
            f"{a[k]:10.2f}" if isinstance(a.get(k), (int, float)) else f"{str(a.get(k))[:10]:>10}" for k, _ in L2_COLS))
    return "\n".join(lines)


# ---------------------------------------------------------------- the decision logic (shared by run + replay)
class Detector:
    """One day's state machine. step() takes a timestamp + source and returns seconds to wait.
    A visit ends after active.leave_after_s with no person (Clef-omni AND the gate model agree);
    then Layer 2 reads the whole day's timeline and decides."""

    def __init__(self, cfg, rec_dir):
        self.cfg, self.rec_dir = cfg, rec_dir
        self.mode, self.last_seen, self.visit, self.done, self.result, self.photo = "idle", None, [], False, None, None
        os.makedirs(rec_dir, exist_ok=True)
        path = os.path.join(rec_dir, "calls.jsonl")
        # the day's Layer 1 timeline survives restarts: reload today's active observations
        self.timeline = [r for r in (json.loads(l) for l in open(path)) if r["phase"] == "active"] if os.path.exists(path) else []
        self.calls = open(path, "a")

    def _record(self, now, phase, res, full, crop, keep):
        stamp = now.strftime("%H%M%S")
        saved = None
        if keep and full:
            saved = os.path.join(self.rec_dir, f"{stamp}_{phase}.jpg")
            shutil.copy(full, saved)
            if crop:
                shutil.copy(crop, os.path.join(self.rec_dir, f"{stamp}_{phase}_crop.jpg"))
        row = {"time": now.isoformat(timespec="seconds"), "phase": phase, "ms": res.get("_ms"),
               "tokens": res.get("usage", {}).get("input_tokens"), "answers": simple(res), "frame": saved}
        self.calls.write(json.dumps(row) + "\n"); self.calls.flush()
        return row

    def _person_gate(self, now, full):
        g = self.cfg["gate"]
        res = clef(self.cfg, g["model"], "A frame from a fixed CCTV camera in a utility room.", GATE_Q, [full])
        return simple(res)["person_present"], res

    def step(self, now, src=None, offset_s=None):
        g, a = self.cfg["gate"], self.cfg["active"]
        with tempfile.TemporaryDirectory() as tmp:
            if self.mode == "idle":
                full, _ = grab(self.cfg, "gate", tmp, offset_s, with_crop=False, src=src)
                p, res = self._person_gate(now, full)
                self._record(now, "gate", res, full, None,
                             keep=p >= g["threshold"] or (now.minute % 5 == 0 and now.second < g["interval_s"]))
                log(f"gate  person={p:.2f}")
                if p < g["threshold"]:
                    return g["interval_s"]
                self.mode, self.last_seen, self.visit = "active", now, []
                log("-> ACTIVE (person present)")
            full, crop = grab(self.cfg, "active", tmp, offset_s, with_crop=True, src=src)
            res = clef(self.cfg, a["model"], STATE_TEXT, ACTIVE_Q, [full, crop] if crop else [full])
            row = self._record(now, "active", res, full, crop, keep=True)
            s = row["answers"]
            present = s["person_count"] >= 0.5
            if not present:  # Clef-omni misses hidden/high-up people; confirm with the gate model before ending a visit
                p, gres = self._person_gate(now, full)
                self._record(now, "presence_check", gres, full, None, keep=False)
                present = p >= g["threshold"]
        self.timeline.append(row)
        self.visit.append(row)
        if present:
            self.last_seen = now
        log(f"active persons~{s['person_count']:.1f}{'' if s['person_count'] >= 0.5 else (' (gate: present)' if present else ' (gate: absent)')}"
            f" hands={s['hands_at_tank_opening']:.2f} hose={s['hose_or_pipe_held_by_person']:.2f} rod={s['rod_held_by_person']:.2f}")
        if (now - self.last_seen).total_seconds() >= a["leave_after_s"]:
            log("-> visit ended (no person for %ss): Layer 2" % a["leave_after_s"])
            self.decide(now)
            self.mode, self.visit = "idle", []
            return g["interval_s"]
        return a["interval_s"]

    def _best_frame(self, rows):
        rows = [r for r in rows if r.get("frame")]
        if not rows:
            return None
        key = lambda r: r["answers"]["hands_at_tank_opening"] + max(r["answers"]["hose_or_pipe_held_by_person"],
                                                                     r["answers"]["rod_held_by_person"])
        return max(rows, key=key)["frame"]

    def decide(self, now):
        """Layer 2 over the whole day's timeline. Falls back to the frame-count rule if Clef is unreachable."""
        if not self.timeline:
            return
        l2 = self.cfg["layer2"]
        try:
            state = L2_STATE + "\nToday's observations:\n" + l2_table(self.timeline, l2.get("max_rows", 400))
            res = clef(self.cfg, l2["model"], state, L2_Q, [])
            act = res["answers"]["activity"]
            probs = act["probabilities"]
            choice, p = act["choice"], probs[act["choice"]]
            self._record(now, "layer2", res, None, None, keep=False)
            log(f"LAYER 2 -> {choice} {p:.2f}  " + "  ".join(f"{k}:{v:.2f}" for k, v in probs.items()))
            if choice in ("measurement", "refill") and p >= l2["threshold"]:
                self.done, self.photo = True, self._best_frame(self.visit) or self._best_frame(self.timeline)
                self.result = (f"Tank activity: looks like {choice.upper()} (probability {p:.2f}; "
                               + ", ".join(f"{k} {v:.2f}" for k, v in probs.items())
                               + f"). Decided at {now:%H:%M} from {len(self.timeline)} observations. "
                                 "Parallel test detector, not the official alarm.")
            self.last_l2 = (choice, probs)
        except Exception as e:
            log(f"LAYER 2 failed ({e}); using the frame-count rule")
            d = self.cfg["decision"]
            q = [r for r in self.visit if r["answers"]["hands_at_tank_opening"] >= d["hands_min"]
                 and max(r["answers"]["hose_or_pipe_held_by_person"], r["answers"]["rod_held_by_person"]) >= d["object_min"]]
            if len(q) >= d["frames_needed"]:
                margin = sum(r["answers"]["rod_held_by_person"] - r["answers"]["hose_or_pipe_held_by_person"] for r in q) / len(q)
                kind = "MEASUREMENT (rod)" if margin > 0 else "REFILL (hose)"
                self.done, self.photo = True, self._best_frame(q)
                self.result = (f"Tank activity: looks like {kind} (fallback rule: {len(q)} frames, rod-vs-hose {margin:+.2f}; "
                               "Layer 2 was unavailable). Parallel test detector, not the official alarm.")


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
            if det is not None and det_day == today and not det.done:
                det.cfg = cfg
                det.decide(now)  # last look over everything seen today
            if det is not None and det.done:
                telegram(cfg, det.result, photo=det.photo)
                json.dump({**st, "finished": True, "result": det.result}, open(sp, "w")); continue
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
            wait = det.step(now)  # each layer pulls its own stream
            errors = 0
        except Exception as e:
            errors += 1
            log(f"ERROR ({errors} in a row): {e}")
            if errors == cfg.get("error_alert_after", 10):
                telegram(cfg, f"Problem: {errors} failures in a row, latest: {str(e)[:200]}")
            wait = 30
        if det.done:
            telegram(cfg, det.result, photo=det.photo)
            json.dump({**st, "finished": True, "result": det.result}, open(sp, "w"))
            continue
        time.sleep(max(0, wait - (time.time() - t0)))


def cmd_replay(path, start, full=False):
    """Same Detector over a recorded clip on a virtual clock. Real Clef calls, Telegram printed only.
    full=True never confirms early, so the whole clip becomes a Layer 1 timeline (for Layer 2 tests)."""
    cfg = load_config()
    if full:
        cfg["layer2"]["threshold"] = 2.0  # never confirm: run Layer 1 over the whole clip
    dur = float(subprocess.run([ffmpeg_bin(cfg).replace("ffmpeg", "ffprobe"), "-v", "error", "-show_entries",
                                "format=duration", "-of", "csv=p=0", path], capture_output=True, text=True).stdout)
    t0 = dt.datetime.combine(dt.date.today(), dt.datetime.strptime(start, "%H:%M:%S").time())
    name = os.path.splitext(os.path.basename(path))[0]
    rec = os.path.join(HERE, "records", f"replay_{'full_' if full else ''}{name}")
    shutil.rmtree(rec, ignore_errors=True)  # replays start clean
    det = Detector(cfg, rec)
    off, calls = 0.0, 0
    while off < dur - 0.5 and not det.done:
        now = t0 + dt.timedelta(seconds=off)
        print(f"[{now:%H:%M:%S}] ", end="")
        wait = det.step(now, path, offset_s=off)
        calls += 1
        off += wait
    if not det.done and det.mode == "active":
        log("-> end of clip with a visit in progress: Layer 2")
        det.decide(t0 + dt.timedelta(seconds=off))
    if det.done:
        telegram(cfg, det.result, dry=True)
    else:
        log(f"end of clip, no activity confirmed ({calls} steps)")


def cmd_selftest(notify):
    cfg = load_config()
    ok = True
    for layer in ["gate", "active"]:
        if not stream(cfg, layer)[0]:
            print(f"FAIL config: no rtsp_url for the {layer} layer"); ok = False
    for k in ["_account", "_token"]:
        if not cfg.get(k):
            print(f"FAIL config: {k[1:]} missing from .env"); ok = False
    with tempfile.TemporaryDirectory() as tmp:
        try:
            url, asp = stream(cfg, "gate")
            full, _ = grab(cfg, "gate", tmp, with_crop=False)
            shutil.copy(full, os.path.join(HERE, "selftest_gate.jpg"))
            r = clef(cfg, cfg["gate"]["model"], "A frame from a fixed CCTV camera in a utility room.", GATE_Q, [full])
            print(f"OK   gate stream (aspect {asp}) -> selftest_gate.jpg; {cfg['gate']['model']}: "
                  f"person_present={simple(r)['person_present']:.2f}, {r['_ms']} ms, {r.get('usage', {}).get('input_tokens')} tokens")
        except Exception as e:
            print(f"FAIL gate: {e}"); ok = False
    with tempfile.TemporaryDirectory() as tmp:
        try:
            url, asp = stream(cfg, "active")
            full, crop = grab(cfg, "active", tmp)
            shutil.copy(full, os.path.join(HERE, "selftest_frame.jpg"))
            if crop: shutil.copy(crop, os.path.join(HERE, "selftest_crop.jpg"))
            r = clef(cfg, cfg["active"]["model"], STATE_TEXT, ACTIVE_Q, [full, crop] if crop else [full])
            print(f"OK   active stream (aspect {asp}) -> selftest_frame.jpg + selftest_crop.jpg (crop must show the tank tops; "
                  f"people/tanks must not look stretched); {cfg['active']['model']}: {r['_ms']} ms, {r.get('usage', {}).get('input_tokens')} tokens")
            r = clef(cfg, cfg["layer2"]["model"], L2_STATE + "\nToday's observations:\n(none yet)", L2_Q, [])
            print(f"OK   layer2 ({cfg['layer2']['model']}): {r['_ms']} ms")
        except Exception as e:
            print(f"FAIL active/layer2: {e}"); ok = False
    if notify:
        telegram(cfg, "Self-test from the parallel tank detector (no action needed).")
        print("sent Telegram test message")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    args = sys.argv[1:]
    if not args or args[0] == "run":
        cmd_run()
    elif args[0] == "replay":
        cmd_replay(args[1], args[2], full="--full" in args)
    elif args[0] == "selftest":
        cmd_selftest("--notify" in args)
    else:
        print(__doc__); sys.exit(2)
