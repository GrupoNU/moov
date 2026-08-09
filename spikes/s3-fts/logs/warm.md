<!-- median account = 29, frequent sender = samantha -->

### Warm-cache latency (n=50 per cell)

| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | common word + date DESC | 1 | 1000000 | 50 | 5.5 | 9.3 | 9.8 | 3.6 | 9.8 |
| 1 | common word + date DESC | 29 | 26640 | 50 | 5.7 | 9.0 | 21.4 | 3.0 | 21.4 |
| 2 | rare word + date DESC | 1 | 1000000 | 10 | 1.5 | 3.3 | 6.9 | 1.0 | 6.9 |
| 2 | rare word + date DESC | 29 | 26640 | 0 | 1.5 | 3.7 | 4.8 | 0.7 | 4.8 |
| 3 | two-word AND + date DESC | 1 | 1000000 | 50 | 12.9 | 16.4 | 30.9 | 10.7 | 30.9 |
| 3 | two-word AND + date DESC | 29 | 26640 | 50 | 6.1 | 9.7 | 23.8 | 4.4 | 23.8 |
| 4 | phrase + date DESC | 1 | 1000000 | 1 | 1.3 | 3.1 | 5.6 | 0.7 | 5.6 |
| 4 | phrase + date DESC | 29 | 26640 | 0 | 1.3 | 4.0 | 6.8 | 0.8 | 6.8 |
| 5 | prefix (search-as-you-type) + date DESC | 1 | 1000000 | 50 | 2.8 | 5.0 | 5.5 | 1.4 | 5.5 |
| 5 | prefix (search-as-you-type) + date DESC | 29 | 26640 | 50 | 2.1 | 4.8 | 5.0 | 1.2 | 5.0 |
| 6 | common word + mailbox + last 90 days | 1 | 1000000 | 50 | 4.9 | 6.8 | 7.8 | 2.6 | 7.8 |
| 6 | common word + mailbox + last 90 days | 29 | 26640 | 50 | 5.6 | 7.2 | 19.9 | 3.2 | 19.9 |
| 7 | common word + unread only | 1 | 1000000 | 50 | 18.2 | 23.6 | 37.0 | 15.7 | 37.0 |
| 7 | common word + unread only | 29 | 26640 | 50 | 11.2 | 14.9 | 27.1 | 9.9 | 27.1 |
| 8 | from-address search (weight B) | 1 | 1000000 | 50 | 10.5 | 14.7 | 30.4 | 8.5 | 30.4 |
| 8 | from-address search (weight B) | 29 | 26640 | 50 | 10.7 | 14.2 | 18.2 | 8.4 | 18.2 |
| 9 | two-word AND + ts_rank_cd relevance | 1 | 1000000 | 50 | 778.8 | 892.0 | 1199.8 | 708.2 | 1199.8 |
| 9 | two-word AND + ts_rank_cd relevance | 29 | 26640 | 50 | 22.3 | 27.6 | 29.7 | 18.5 | 29.7 |
| 10 | exact count for common word | 1 | 1000000 | 1 | 383.3 | 451.9 | 484.8 | 308.6 | 484.8 |
| 10 | exact count for common word | 29 | 26640 | 1 | 10.2 | 14.1 | 22.8 | 8.5 | 22.8 |

__COMPLETE__
