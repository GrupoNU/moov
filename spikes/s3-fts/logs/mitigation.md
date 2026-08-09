<!-- median account = 29, frequent sender = samantha -->

### Mitigation warm-cache latency (n=30 per cell)

| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 101 | MITIGATION #9: rank over top-500-by-date candidates | 1 | 1000000 | 50 | 160.2 | 265.4 | 294.4 | 126.7 | 294.4 |
| 101 | MITIGATION #9: rank over top-500-by-date candidates | 29 | 26640 | 50 | 25.3 | 44.6 | 48.7 | 18.9 | 48.7 |
| 102 | MITIGATION #9: rank over top-200-by-date candidates | 1 | 1000000 | 50 | 63.6 | 134.0 | 162.0 | 55.4 | 162.0 |
| 102 | MITIGATION #9: rank over top-200-by-date candidates | 29 | 26640 | 50 | 22.5 | 37.5 | 38.6 | 17.2 | 38.6 |
| 103 | MITIGATION #10: capped count (LIMIT 1000 — '999+') | 1 | 1000000 | 1 | 130.8 | 190.7 | 209.3 | 105.6 | 209.3 |
| 103 | MITIGATION #10: capped count (LIMIT 1000 — '999+') | 29 | 26640 | 1 | 8.5 | 24.2 | 28.2 | 5.7 | 28.2 |
| 104 | MITIGATION #10: capped count (LIMIT 200 — '199+') | 1 | 1000000 | 1 | 19.1 | 98.3 | 160.1 | 15.2 | 160.1 |
| 104 | MITIGATION #10: capped count (LIMIT 200 — '199+') | 29 | 26640 | 1 | 6.2 | 19.7 | 21.1 | 3.8 | 21.1 |

__COMPLETE__
