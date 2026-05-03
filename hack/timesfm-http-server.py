#!/usr/bin/env python3
"""Small TimesFM HTTP runner for Skale local/demo clusters.

Protocol:
  POST /forecast
  {
    "stepSeconds": 30,
    "horizonPoints": 10,
    "series": [{"timestamp": "...", "value": 123.4}]
  }

Response:
  {"model": "timesfm", "values": [124.0, 125.0]}

This process owns the Python/PyTorch/TimesFM runtime so the controller image can
stay a small Go binary.
"""

from __future__ import annotations

import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import numpy as np
import timesfm

try:
    import torch
except ImportError:  # pragma: no cover - torch comes from timesfm[torch]
    torch = None


MODEL_ID = os.getenv("SKALE_TIMESFM_MODEL", "google/timesfm-2.5-200m-pytorch")
HOST = os.getenv("SKALE_TIMESFM_HOST", "0.0.0.0")
PORT = int(os.getenv("SKALE_TIMESFM_PORT", "8080"))
MAX_CONTEXT = int(os.getenv("SKALE_TIMESFM_MAX_CONTEXT", "1024"))
MAX_HORIZON = int(os.getenv("SKALE_TIMESFM_MAX_HORIZON", "256"))


class TimesFMRunner:
    def __init__(self) -> None:
        if torch is not None:
            torch.set_float32_matmul_precision("high")
        self._model = timesfm.TimesFM_2p5_200M_torch.from_pretrained(MODEL_ID)
        self._model.compile(
            timesfm.ForecastConfig(
                max_context=MAX_CONTEXT,
                max_horizon=MAX_HORIZON,
                normalize_inputs=True,
                use_continuous_quantile_head=True,
                force_flip_invariance=True,
                infer_is_positive=True,
                fix_quantile_crossing=True,
            )
        )
        self._lock = threading.Lock()

    def forecast(self, payload: dict[str, Any]) -> list[float]:
        series = payload.get("series") or []
        horizon = int(payload.get("horizonPoints") or 0)
        if not series:
            raise ValueError("series is required")
        if horizon <= 0:
            raise ValueError("horizonPoints must be positive")
        if horizon > MAX_HORIZON:
            raise ValueError(f"horizonPoints {horizon} exceeds max horizon {MAX_HORIZON}")
        if len(series) > MAX_CONTEXT:
            series = series[-MAX_CONTEXT:]
        values = np.asarray([float(point.get("value", 0.0)) for point in series], dtype=np.float32)
        with self._lock:
            point_forecast, _ = self._model.forecast(horizon=horizon, inputs=[values])
        return [float(value) for value in point_forecast[0][:horizon]]


runner = TimesFMRunner()


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path != "/healthz":
            self.send_error(404)
            return
        self._json(200, {"ok": True, "model": MODEL_ID})

    def do_POST(self) -> None:
        if self.path != "/forecast":
            self.send_error(404)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length))
            self._json(200, {"model": "timesfm", "values": runner.forecast(payload)})
        except Exception as exc:  # Keep wire contract simple for controller.
            self._json(400, {"model": "timesfm", "error": str(exc)})

    def log_message(self, fmt: str, *args: object) -> None:
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)

    def _json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def main() -> int:
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print(f"timesfm runner listening on {HOST}:{PORT} model={MODEL_ID}", flush=True)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
