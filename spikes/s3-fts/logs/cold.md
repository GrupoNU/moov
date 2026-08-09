<!-- median account = 29, frequent sender = samantha -->

### Cold-cache latency (n=10 per cell)

| # | Shape | Account | Msgs | Rows | p50 ms | p95 ms | p99 ms | min | max |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | common word + date DESC | 1 | 1000000 | 50 | 4.9 | 396.1 | 396.1 | 4.5 | 396.1 |
| 1 | common word + date DESC | 29 | 26640 | 50 | 5.2 | 774.8 | 774.8 | 3.6 | 774.8 |
| 2 | rare word + date DESC | 1 | 1000000 | 10 | 1.3 | 16.3 | 16.3 | 1.0 | 16.3 |
| 2 | rare word + date DESC | 29 | 26640 | 0 | 1.3 | 2.6 | 2.6 | 0.7 | 2.6 |
| 3 | two-word AND + date DESC | 1 | 1000000 | 50 | 11.2 | 651.7 | 651.7 | 9.7 | 651.7 |
| 3 | two-word AND + date DESC | 29 | 26640 | 50 | 6.5 | 125.9 | 125.9 | 4.7 | 125.9 |
| 4 | phrase + date DESC | 1 | 1000000 | 1 | 1.4 | 26.1 | 26.1 | 0.9 | 26.1 |
| 4 | phrase + date DESC | 29 | 26640 | 0 | 2.1 | 5.5 | 5.5 | 1.0 | 5.5 |
| 5 | prefix (search-as-you-type) + date DESC | 1 | 1000000 | 50 | 3.4 | 13.7 | 13.7 | 2.7 | 13.7 |
| 5 | prefix (search-as-you-type) + date DESC | 29 | 26640 | 50 | 2.1 | 4.1 | 4.1 | 1.3 | 4.1 |
| 6 | common word + mailbox + last 90 days | 1 | 1000000 | 50 | 5.0 | 21.3 | 21.3 | 4.5 | 21.3 |
| 6 | common word + mailbox + last 90 days | 29 | 26640 | 50 | 3.9 | 113.0 | 113.0 | 2.7 | 113.0 |
| 7 | common word + unread only | 1 | 1000000 | 50 | 14.0 | 323.6 | 323.6 | 12.9 | 323.6 |
| 7 | common word + unread only | 29 | 26640 | 50 | 7.4 | 113.9 | 113.9 | 6.8 | 113.9 |
| 8 | from-address search (weight B) | 1 | 1000000 | 50 | 6.4 | 10.5 | 10.5 | 5.1 | 10.5 |
| 8 | from-address search (weight B) | 29 | 26640 | 50 | 9.7 | 290.5 | 290.5 | 8.4 | 290.5 |
| 9 | two-word AND + ts_rank_cd relevance | 1 | 1000000 | 50 | 678.7 | 32503.7 | 32503.7 | 652.4 | 32503.7 |
| 9 | two-word AND + ts_rank_cd relevance | 29 | 26640 | 50 | 20.4 | 819.8 | 819.8 | 18.0 | 819.8 |
| 10 | exact count for common word | 1 | 1000000 | 1 | 322.1 | 502.9 | 502.9 | 305.2 | 502.9 |
| 10 | exact count for common word | 29 | 26640 | 1 | 7.8 | 9.3 | 9.3 | 6.4 | 9.3 |

__COMPLETE__
