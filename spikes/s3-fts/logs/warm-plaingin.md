<!-- median account = 29, frequent sender = samantha -->

### Warm-cache latency (n=50 per cell)

| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | common word + date DESC | 1 | 1000000 | 50 | 5.1 | 12.2 | 21.7 | 3.3 | 21.7 |
| 1 | common word + date DESC | 29 | 26640 | 50 | 4.6 | 7.6 | 23.5 | 2.8 | 23.5 |
| 2 | rare word + date DESC | 1 | 1000000 | 10 | 10636.7 | 12982.8 | 13617.8 | 9503.6 | 13617.8 |
| 2 | rare word + date DESC | 29 | 26640 | 0 | 3.8 | 5.4 | 7.0 | 2.2 | 7.0 |
| 3 | two-word AND + date DESC | 1 | 1000000 | 50 | 12.0 | 15.7 | 26.3 | 9.1 | 26.3 |
| 3 | two-word AND + date DESC | 29 | 26640 | 50 | 171.3 | 214.5 | 225.5 | 154.2 | 225.5 |
| 4 | phrase + date DESC | 1 | 1000000 | 1 | 0.8 | 1.8 | 1.8 | 0.4 | 1.8 |
| 4 | phrase + date DESC | 29 | 26640 | 0 | 0.8 | 3.5 | 4.5 | 0.4 | 4.5 |
| 5 | prefix (search-as-you-type) + date DESC | 1 | 1000000 | 50 | 1.7 | 3.4 | 5.4 | 1.0 | 5.4 |
| 5 | prefix (search-as-you-type) + date DESC | 29 | 26640 | 50 | 1.4 | 2.7 | 2.8 | 0.7 | 2.8 |
| 6 | common word + mailbox + last 90 days | 1 | 1000000 | 50 | 3.8 | 5.9 | 6.3 | 1.9 | 6.3 |
| 6 | common word + mailbox + last 90 days | 29 | 26640 | 50 | 2.5 | 5.7 | 5.9 | 1.6 | 5.9 |
| 7 | common word + unread only | 1 | 1000000 | 50 | 16.6 | 19.6 | 28.1 | 13.2 | 28.1 |
| 7 | common word + unread only | 29 | 26640 | 50 | 350.9 | 395.3 | 404.3 | 299.5 | 404.3 |
| 8 | from-address search (weight B) | 1 | 1000000 | 50 | 8.1 | 13.4 | 16.9 | 6.1 | 16.9 |
| 8 | from-address search (weight B) | 29 | 26640 | 50 | 10.3 | 14.9 | 16.9 | 7.7 | 16.9 |
| 9 | two-word AND + ts_rank_cd relevance | 1 | 1000000 | 50 | 1179.9 | 1415.3 | 1451.7 | 1028.7 | 1451.7 |
| 9 | two-word AND + ts_rank_cd relevance | 29 | 26640 | 50 | 156.7 | 180.0 | 199.8 | 122.1 | 199.8 |
| 10 | exact count for common word | 1 | 1000000 | 1 | 995.9 | 1147.5 | 1187.5 | 832.4 | 1187.5 |
| 10 | exact count for common word | 29 | 26640 | 1 | 377.5 | 553.5 | 624.2 | 309.7 | 624.2 |

__COMPLETE__
