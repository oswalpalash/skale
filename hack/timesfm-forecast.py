#!/usr/bin/env python3
"""TimesFM JSON runner for Skale's external forecast command protocol.

The controller calls this process with JSON on stdin and expects JSON on stdout:

Input:
  {
    "stepSeconds": 30,
    "horizonPoints": 10,
    "series": [{"timestamp": "...", "value": 123.4}]
  }

Output:
  {"model": "timesfm", "values": [124.0, 125.0]}

Install the TimesFM package and a backend in the runtime that executes this
script. This file is intentionally not baked into the default distroless
controller image.
"""

import json
import sys

import numpy as np
import timesfm


MODEL_ID = "google/timesfm-2.5-200m-pytorch"


def main() -> int:
    request = json.load(sys.stdin)
    series = request.get("series") or []
    horizon = int(request.get("horizonPoints") or 0)
    if not series:
        return fail("series is required")
    if horizon <= 0:
        return fail("horizonPoints must be positive")

    values = np.asarray([float(point.get("value", 0.0)) for point in series], dtype=np.float32)
    model = timesfm.TimesFM_2p5_200M_torch.from_pretrained(MODEL_ID)
    model.compile(
        timesfm.ForecastConfig(
            max_context=max(1024, len(values)),
            max_horizon=max(256, horizon),
            normalize_inputs=True,
            use_continuous_quantile_head=True,
            force_flip_invariance=True,
            infer_is_positive=True,
            fix_quantile_crossing=True,
        )
    )
    point_forecast, _ = model.forecast(horizon=horizon, inputs=[values])
    json.dump(
        {
            "model": "timesfm",
            "values": [float(value) for value in point_forecast[0][:horizon]],
        },
        sys.stdout,
    )
    return 0


def fail(message: str) -> int:
    json.dump({"model": "timesfm", "error": message}, sys.stdout)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
