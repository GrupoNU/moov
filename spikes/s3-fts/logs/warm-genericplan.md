<!-- median account = 29, frequent sender = samantha -->

### Warm-cache latency (n=50 per cell)

| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | common word + date DESC | 1 | 1000000 | 50 | 4.6 | 6.6 | 8.2 | 2.2 | 8.2 |
| 1 | common word + date DESC | 29 | 26640 | 50 | 5.3 | 7.5 | 9.4 | 2.4 | 9.4 |
| 2 | rare word + date DESC | 1 | 1000000 | 10 | 10314.7 | 11501.5 | 12470.2 | 232.8 | 12470.2 |
| 2 | rare word + date DESC | 29 | 26640 | 0 | 3.2 | 6.2 | 7.5 | 2.3 | 7.5 |
| 3 | two-word AND + date DESC | 1 | 1000000 | 50 | 618.2 | 759.0 | 775.0 | 480.3 | 775.0 |
| 3 | two-word AND + date DESC | 29 | 26640 | 50 | 172.9 | 195.3 | 226.4 | 154.4 | 226.4 |
| 4 | phrase + date DESC | 1 | 1000000 | 1 | 271.4 | 333.7 | 361.0 | 188.7 | 361.0 |
| 4 | phrase + date DESC | 29 | 26640 | 0 | 3.4 | 4.9 | 5.2 | 1.9 | 5.2 |
| 5 | prefix (search-as-you-type) + date DESC | 1 | 1000000 | 50 | 2.6 | 4.2 | 6.1 | 1.2 | 6.1 |
| 5 | prefix (search-as-you-type) + date DESC | 29 | 26640 | 50 | 2.1 | 4.2 | 4.3 | 1.2 | 4.3 |
| 6 | common word + mailbox + last 90 days | 1 | 1000000 | 50 | 438.4 | 537.0 | 633.2 | 372.6 | 633.2 |
| 6 | common word + mailbox + last 90 days | 29 | 26640 | 50 | 362.4 | 441.6 | 614.4 | 296.1 | 614.4 |
| 7 | common word + unread only | 1 | 1000000 | 50 | 1047.8 | 1211.9 | 1447.7 | 808.7 | 1447.7 |
| 7 | common word + unread only | 29 | 26640 | 50 | 380.4 | 435.2 | 534.4 | 325.5 | 534.4 |
| 8 | from-address search (weight B) | 1 | 1000000 | 50 | 628.2 | 777.9 | 835.1 | 496.3 | 835.1 |
| 8 | from-address search (weight B) | 29 | 26640 | 50 | 170.7 | 206.2 | 214.2 | 143.2 | 214.2 |
| 9 | two-word AND + ts_rank_cd relevance | 1 | 1000000 | 50 | 1361.1 | 1548.8 | 1643.2 | 1191.5 | 1643.2 |
| 9 | two-word AND + ts_rank_cd relevance | 29 | 26640 | 50 | 169.6 | 203.3 | 220.1 | 148.2 | 220.1 |
| 10 | exact count for common word | 1 | 1000000 | 1 | 957.6 | 1097.0 | 1140.0 | 836.7 | 1140.0 |
| 10 | exact count for common word | 29 | 26640 | 1 | 336.2 | 390.2 | 427.3 | 302.1 | 427.3 |

