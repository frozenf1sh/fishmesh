# FishMesh benchmark comparison

- TTFT P95 delta: -55.42%
- Bootstrap 95% CI: [-67.28%, -31.52%] (20000 samples, seed 20260831)

| Arm | Runs | Success | Failed | P50 | P95 | P99 | Run median P95 | Static MAE | Static abs P95 | Learned MAE | Learned abs P95 | Agree | Paired learned-static |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| baseline | 2 | 42 | 0 | 1291.07 | 3973.91 | 4471.19 | 3973.91 | 0.00 | 0.00 | 0.00 | 0.00 | 0.0% | 0.00 |
| treatment | 2 | 42 | 0 | 1150.52 | 1771.68 | 2031.37 | 1771.68 | 0.00 | 0.00 | 0.00 | 0.00 | 0.0% | 0.00 |

## Gateway capacity evidence

| Arm | Reports | Valid windows | Accepted QPS | Completed QPS | Rejection QPS | Average in-flight | Little's Law W ms |
|---|---:|---:|---:|---:|---:|---:|---:|
| baseline | 2 | 2 | 1.303 | 1.303 | 0.000 | 2.554 | 1959.61 |
| treatment | 2 | 2 | 1.941 | 1.941 | 0.000 | 2.355 | 1213.02 |
