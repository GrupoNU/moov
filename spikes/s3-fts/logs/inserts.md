<!-- median account = 29, frequent sender = samantha -->

### Incremental inserts — fastupdate = true

| Batch | Insert ms (100 rows) | Post-batch search ms |
|---:|---:|---:|
| 1 | 77.8 | 4.8 |
| 2 | 27.2 | 1.5 |
| 3 | 22.5 | 1.6 |
| 4 | 25.7 | 2.6 |
| 5 | 27.6 | 3.0 |
| 6 | 24.7 | 1.4 |
| 7 | 28.6 | 2.5 |
| 8 | 24.8 | 2.8 |
| 9 | 26.8 | 1.9 |
| 10 | 27.9 | 2.3 |
| 11 | 47.9 | 2.6 |
| 16 | 32.0 | 2.6 |
| 21 | 18.6 | 1.5 |
| 26 | 22.9 | 2.9 |

**Insert p50/p95/max: 25.2 / 47.9 / 77.8 ms.  Post-batch search p50/p95/max: 2.4 / 6.2 / 6.4 ms**

### Incremental inserts — fastupdate = false

| Batch | Insert ms (100 rows) | Post-batch search ms |
|---:|---:|---:|
| 1 | 121.8 | 4.7 |
| 2 | 54.9 | 2.1 |
| 3 | 52.9 | 2.5 |
| 4 | 52.6 | 2.0 |
| 5 | 61.0 | 5.7 |
| 6 | 54.9 | 2.0 |
| 7 | 46.6 | 1.9 |
| 8 | 66.3 | 4.0 |
| 9 | 50.7 | 2.9 |
| 10 | 39.2 | 2.0 |
| 11 | 45.3 | 3.6 |
| 16 | 46.6 | 1.9 |
| 21 | 47.1 | 2.3 |
| 26 | 44.8 | 2.8 |

**Insert p50/p95/max: 48.4 / 68.0 / 121.8 ms.  Post-batch search p50/p95/max: 2.2 / 6.1 / 14.2 ms**

__COMPLETE__
