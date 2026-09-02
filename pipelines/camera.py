"""Grab the current frame from an RTSP camera stream on the LAN.

NOT YET TESTED against a real camera - built against the OpenCV/RTSP API but
this environment has no RTSP source to validate against. Known gotcha handled
below: cv2.VideoCapture buffers frames internally, so the first read() after
opening a stream can return a stale frame from the decode buffer rather than
the current one - we read and discard a few frames before keeping the last.
"""

import time

import cv2


def grab_frame(rtsp_url: str, save_path: str, flush_frames: int = 5, connect_timeout: float = 10.0) -> str:
    if rtsp_url == "TBD" or not rtsp_url:
        raise RuntimeError("RTSP URL not configured (still 'TBD') - set it in .env before calling grab_frame")

    cap = cv2.VideoCapture(rtsp_url)
    try:
        cap.set(cv2.CAP_PROP_BUFFERSIZE, 1)  # not all backends honor this, hence the manual flush below too
    except Exception:
        pass

    start = time.monotonic()
    if not cap.isOpened():
        cap.release()
        raise RuntimeError(f"Could not open RTSP stream: {rtsp_url}")

    frame = None
    for _ in range(flush_frames):
        if time.monotonic() - start > connect_timeout:
            cap.release()
            raise RuntimeError(f"Timed out reading from RTSP stream: {rtsp_url}")
        ret, frame = cap.read()
        if not ret:
            cap.release()
            raise RuntimeError(f"Could not read frame from RTSP stream: {rtsp_url}")

    cap.release()
    cv2.imwrite(save_path, frame)
    return save_path
