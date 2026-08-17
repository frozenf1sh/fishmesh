#!/usr/bin/env python3
"""Generate the checked-in R6I-31 v3 benchmark charts without third-party packages."""

from __future__ import annotations

import html
import json
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
DATA_ROOT = REPO_ROOT / "artifacts/bench/r6i31-conversation-ladder-28k-v3"
OUTPUT_ROOT = REPO_ROOT / "docs/benchmarks/r6i31-conversation-ladder-28k"

NAVY = "#18324b"
TEAL = "#0f766e"
BLUE = "#2563eb"
GRAY = "#64748b"
LIGHT = "#e2e8f0"
PALE = "#f8fafc"


def percentile(values: list[int], point: int) -> int:
    ordered = sorted(values)
    if not ordered:
        return 0
    return ordered[(len(ordered) - 1) * point // 100]


def read_json(path: Path) -> dict:
    return json.loads(path.read_text())


def request_values(arm: str, field: str) -> list[int]:
    values: list[int] = []
    for replicate in ("r1", "r2"):
        path = DATA_ROOT / "runs" / f"{replicate}-{arm}" / "bench/requests.jsonl"
        for line in path.read_text().splitlines():
            record = json.loads(line)
            if record.get("record_type") != "request" or record.get("status_code") != 200:
                continue
            if field == "cached_prefix_tokens":
                values.append(int(record.get("headers", {}).get(field, 0)))
            else:
                values.append(int(record.get(field, 0)))
    return values


def load_data() -> dict:
    comparison = read_json(DATA_ROOT / "compare/comparison.json")
    cached = {arm: request_values(arm, "cached_prefix_tokens") for arm in ("la", "kv")}
    cache_samples = {}
    for arm in ("la", "kv"):
        cache_samples[arm] = sum(
            1
            for replicate in ("r1", "r2")
            for line in (DATA_ROOT / "runs" / f"{replicate}-{arm}" / "bench/requests.jsonl").read_text().splitlines()
            if json.loads(line).get("record_type") == "request"
            and json.loads(line).get("status_code") == 200
            and json.loads(line).get("cached_prefix_sample") is True
        )

    return {
        "ttft": {
            "metrics": ["P50", "P95", "P99"],
            "load-aware-v1": [comparison["baseline"]["ttft_p50_ms"], comparison["baseline"]["ttft_p95_ms"], comparison["baseline"]["ttft_p99_ms"]],
            "kv-aware-v1": [comparison["treatment"]["ttft_p50_ms"], comparison["treatment"]["ttft_p95_ms"], comparison["treatment"]["ttft_p99_ms"]],
            "delta_p95_percent": comparison["ttft_p95_delta_percent"],
            "ci": [comparison["bootstrap_ci_low_percent"], comparison["bootstrap_ci_high_percent"]],
        },
        "capacity": {
            "metrics": ["Accepted QPS", "Little's Law W (ms)", "Average in-flight"],
            "load-aware-v1": [comparison["baseline_gateway"]["accepted_rate_qps"], comparison["baseline_gateway"]["little_law_wait_ms"], comparison["baseline_gateway"]["average_inflight"]],
            "kv-aware-v1": [comparison["treatment_gateway"]["accepted_rate_qps"], comparison["treatment_gateway"]["little_law_wait_ms"], comparison["treatment_gateway"]["average_inflight"]],
        },
        "cache": {
            "metrics": ["Cached samples (%)", "Cached prefix P50 (tokens)", "Cached prefix P95 (tokens)"],
            "load-aware-v1": [100 * cache_samples["la"] / len(cached["la"]), percentile(cached["la"], 50), percentile(cached["la"], 95)],
            "kv-aware-v1": [100 * cache_samples["kv"] / len(cached["kv"]), percentile(cached["kv"], 50), percentile(cached["kv"], 95)],
            "samples": {"load-aware-v1": len(cached["la"]), "kv-aware-v1": len(cached["kv"])},
            "cached_prefix_sum": sum(cached["kv"]),
        },
    }


def esc(value: object) -> str:
    return html.escape(str(value), quote=True)


def svg_header(width: int, height: int, title: str, subtitle: str) -> list[str]:
    return [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
        f'<rect width="{width}" height="{height}" fill="white"/>',
        f'<text x="48" y="42" fill="{NAVY}" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif" font-size="26" font-weight="700">{esc(title)}</text>',
        f'<text x="48" y="70" fill="{GRAY}" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif" font-size="14">{esc(subtitle)}</text>',
    ]


def text(x: float, y: float, value: object, *, size: int = 13, color: str = NAVY, anchor: str = "start", weight: str = "400") -> str:
    return f'<text x="{x:.1f}" y="{y:.1f}" fill="{color}" text-anchor="{anchor}" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif" font-size="{size}px" font-weight="{weight}">{esc(value)}</text>'


def legend(items: list[tuple[str, str]], x: float, y: float) -> list[str]:
    lines: list[str] = []
    cursor = x
    for label, color in items:
        lines.append(f'<rect x="{cursor:.1f}" y="{y - 11:.1f}" width="12" height="12" rx="2" fill="{color}"/>')
        lines.append(text(cursor + 18, y, label, size=13, color=GRAY))
        cursor += 150
    return lines


def grouped_bars(path: Path, title: str, subtitle: str, categories: list[str], series: list[tuple[str, str, list[float]]], *, max_value: float, value_suffix: str = "") -> None:
    width, height = 1200, 680
    left, top, chart_width, chart_height = 90, 130, 1040, 430
    lines = svg_header(width, height, title, subtitle)
    lines.extend(legend([(name, color) for name, color, _ in series], 96, 102))
    for tick in range(6):
        value = max_value * tick / 5
        y = top + chart_height - chart_height * tick / 5
        lines.append(f'<line x1="{left}" y1="{y:.1f}" x2="{left + chart_width}" y2="{y:.1f}" stroke="{LIGHT}" stroke-width="1"/>')
        lines.append(text(left - 12, y + 5, f"{value:.0f}{value_suffix}", size=12, color=GRAY, anchor="end"))
    group_width = chart_width / len(categories)
    bar_width = min(88, group_width / (len(series) + 1.5))
    for category_index, category in enumerate(categories):
        center = left + group_width * (category_index + 0.5)
        total_width = len(series) * bar_width + (len(series) - 1) * 10
        start = center - total_width / 2
        lines.append(text(center, top + chart_height + 34, category, size=14, color=NAVY, anchor="middle", weight="600"))
        for series_index, (_, color, values) in enumerate(series):
            value = values[category_index]
            bar_height = chart_height * value / max_value if max_value else 0
            x = start + series_index * (bar_width + 10)
            y = top + chart_height - bar_height
            lines.append(f'<rect x="{x:.1f}" y="{y:.1f}" width="{bar_width:.1f}" height="{bar_height:.1f}" rx="5" fill="{color}"/>')
            lines.append(text(x + bar_width / 2, y - 10, f"{value:.0f}{value_suffix}", size=12, color=color, anchor="middle", weight="600"))
    lines.append(text(width / 2, height - 58, "Source: artifacts/bench/r6i31-conversation-ladder-28k-v3; 42 successful requests per arm", size=12, color=GRAY, anchor="middle"))
    lines.append("</svg>")
    path.write_text("\n".join(lines) + "\n")


def multi_panel(path: Path, title: str, subtitle: str, metrics: list[str], baseline: list[float], treatment: list[float], max_values: list[float], suffixes: list[str]) -> None:
    width, height = 1500, 620
    lines = svg_header(width, height, title, subtitle)
    lines.extend(legend([("load-aware-v1", GRAY), ("kv-aware-v1", TEAL)], 96, 102))
    panel_width, panel_gap = 430, 35
    top, chart_height = 145, 340
    for index, metric in enumerate(metrics):
        left = 72 + index * (panel_width + panel_gap)
        max_value = max_values[index]
        lines.append(text(left + panel_width / 2, 132, metric, size=15, color=NAVY, anchor="middle", weight="600"))
        for tick in range(5):
            value = max_value * tick / 4
            y = top + chart_height - chart_height * tick / 4
            lines.append(f'<line x1="{left}" y1="{y:.1f}" x2="{left + panel_width}" y2="{y:.1f}" stroke="{LIGHT}" stroke-width="1"/>')
            lines.append(text(left - 10, y + 5, f"{value:.0f}{suffixes[index]}", size=11, color=GRAY, anchor="end"))
        bar_width = 110
        for series_index, (value, color, label) in enumerate(((baseline[index], GRAY, "LA"), (treatment[index], TEAL, "KV"))):
            bar_height = chart_height * value / max_value if max_value else 0
            x = left + 100 + series_index * 170
            y = top + chart_height - bar_height
            lines.append(f'<rect x="{x:.1f}" y="{y:.1f}" width="{bar_width}" height="{bar_height:.1f}" rx="5" fill="{color}"/>')
            lines.append(text(x + bar_width / 2, y - 10, f"{value:.1f}{suffixes[index]}", size=13, color=color, anchor="middle", weight="600"))
            lines.append(text(x + bar_width / 2, top + chart_height + 30, label, size=13, color=NAVY, anchor="middle"))
    lines.append(text(width / 2, height - 48, "Little's Law W = average in-flight / completed QPS; valid Gateway windows in both replicates", size=12, color=GRAY, anchor="middle"))
    lines.append("</svg>")
    path.write_text("\n".join(lines) + "\n")


def main() -> None:
    OUTPUT_ROOT.mkdir(parents=True, exist_ok=True)
    data = load_data()
    (OUTPUT_ROOT / "data.json").write_text(json.dumps(data, indent=2) + "\n")
    ttft_max = 5000
    grouped_bars(
        OUTPUT_ROOT / "ttft-percentiles.svg",
        "R6I-31 v3: TTFT percentiles",
        f"Conversation ladder around 28K; KV-aware P95 delta {data['ttft']['delta_p95_percent']:.2f}% (95% CI {data['ttft']['ci'][0]:.2f}% to {data['ttft']['ci'][1]:.2f}%)",
        data["ttft"]["metrics"],
        [("load-aware-v1", GRAY, data["ttft"]["load-aware-v1"]), ("kv-aware-v1", TEAL, data["ttft"]["kv-aware-v1"])],
        max_value=ttft_max,
        value_suffix=" ms",
    )
    multi_panel(
        OUTPUT_ROOT / "gateway-capacity.svg",
        "R6I-31 v3: Gateway capacity evidence",
        "KV-aware served the same long-context ladder with lower Little's Law waiting time",
        data["capacity"]["metrics"],
        data["capacity"]["load-aware-v1"],
        data["capacity"]["kv-aware-v1"],
        [2.5, 2500, 3.0],
        ["", " ms", ""],
    )
    multi_panel(
        OUTPUT_ROOT / "kv-evidence.svg",
        "R6I-31 v3: KV evidence",
        f"KV-aware: 42/42 cached-prefix samples; {data['cache']['cached_prefix_sum']:,} cached prefix tokens observed",
        data["cache"]["metrics"],
        data["cache"]["load-aware-v1"],
        data["cache"]["kv-aware-v1"],
        [100, 20000, 30000],
        ["%", "", ""],
    )


if __name__ == "__main__":
    main()
