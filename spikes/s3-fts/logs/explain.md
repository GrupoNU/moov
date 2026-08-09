<!-- median account = 29, frequent sender = samantha -->

#### Shape 1 — common word + date DESC — account 1

```
Limit  (cost=0.43..151.55 rows=50 width=55) (actual time=0.177..3.923 rows=50 loops=1)
  Buffers: shared hit=1228
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..380773.48 rows=125985 width=55) (actual time=0.176..3.910 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 328
        Buffers: shared hit=1228
Planning:
  Buffers: shared hit=2
Planning Time: 0.771 ms
Execution Time: 4.011 ms
```

#### Shape 1 — common word + date DESC — account 29

```
Limit  (cost=0.43..228.39 rows=50 width=55) (actual time=0.235..3.480 rows=50 loops=1)
  Buffers: shared hit=1369
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..15328.19 rows=3362 width=55) (actual time=0.233..3.468 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 385
        Buffers: shared hit=1369
Planning:
  Buffers: shared hit=2
Planning Time: 0.788 ms
Execution Time: 3.531 ms
```

#### Shape 2 — rare word + date DESC — account 1

```
Limit  (cost=138.92..139.04 rows=50 width=55) (actual time=0.390..0.392 rows=10 loops=1)
  Buffers: shared hit=43
  ->  Sort  (cost=138.92..139.18 rows=105 width=55) (actual time=0.389..0.390 rows=10 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 26kB
        Buffers: shared hit=43
        ->  Bitmap Heap Scan on messages  (cost=18.48..135.43 rows=105 width=55) (actual time=0.367..0.382 rows=10 loops=1)
              Recheck Cond: ((account_id = 1) AND (tsv @@ '''zanzibarita'''::tsquery))
              Heap Blocks: exact=10
              Buffers: shared hit=43
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..18.46 rows=105 width=0) (actual time=0.357..0.357 rows=10 loops=1)
                    Index Cond: ((account_id = 1) AND (tsv @@ '''zanzibarita'''::tsquery))
                    Buffers: shared hit=33
Planning:
  Buffers: shared hit=2
Planning Time: 0.814 ms
Execution Time: 0.420 ms
```

#### Shape 2 — rare word + date DESC — account 29

```
Limit  (cost=21.32..21.33 rows=3 width=55) (actual time=0.124..0.125 rows=0 loops=1)
  Buffers: shared hit=13
  ->  Sort  (cost=21.32..21.33 rows=3 width=55) (actual time=0.124..0.124 rows=0 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=13
        ->  Bitmap Heap Scan on messages  (cost=17.95..21.29 rows=3 width=55) (actual time=0.119..0.119 rows=0 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''zanzibarita'''::tsquery))
              Buffers: shared hit=13
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..17.95 rows=3 width=0) (actual time=0.114..0.114 rows=0 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''zanzibarita'''::tsquery))
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=2
Planning Time: 0.790 ms
Execution Time: 0.153 ms
```

#### Shape 3 — two-word AND + date DESC — account 1

```
Limit  (cost=0.43..1268.83 rows=50 width=55) (actual time=1.312..10.796 rows=50 loops=1)
  Buffers: shared hit=3757
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..380773.48 rows=15010 width=55) (actual time=1.310..10.779 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
        Rows Removed by Filter: 1104
        Buffers: shared hit=3757
Planning:
  Buffers: shared hit=2
Planning Time: 0.941 ms
Execution Time: 10.850 ms
```

#### Shape 3 — two-word AND + date DESC — account 29

```
Limit  (cost=487.86..487.99 rows=50 width=55) (actual time=3.973..3.984 rows=50 loops=1)
  Buffers: shared hit=872
  ->  Sort  (cost=487.86..488.86 rows=401 width=55) (actual time=3.971..3.976 rows=50 loops=1)
        Sort Key: date DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=872
        ->  Bitmap Heap Scan on messages  (cost=28.39..474.54 rows=401 width=55) (actual time=1.820..3.673 rows=895 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
              Heap Blocks: exact=812
              Buffers: shared hit=872
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..28.29 rows=401 width=0) (actual time=1.701..1.701 rows=895 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                    Buffers: shared hit=60
Planning:
  Buffers: shared hit=2
Planning Time: 0.861 ms
Execution Time: 4.048 ms
```

#### Shape 4 — phrase + date DESC — account 1

```
Limit  (cost=27.41..27.41 rows=1 width=55) (actual time=0.185..0.187 rows=1 loops=1)
  Buffers: shared hit=26
  ->  Sort  (cost=27.41..27.41 rows=1 width=55) (actual time=0.184..0.184 rows=1 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=26
        ->  Bitmap Heap Scan on messages  (cost=26.28..27.40 rows=1 width=55) (actual time=0.103..0.173 rows=1 loops=1)
              Recheck Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
              Filter: (account_id = 1)
              Rows Removed by Filter: 4
              Heap Blocks: exact=5
              Buffers: shared hit=26
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..26.28 rows=1 width=0) (actual time=0.081..0.082 rows=5 loops=1)
                    Index Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=2
Planning Time: 0.995 ms
Execution Time: 0.228 ms
```

#### Shape 4 — phrase + date DESC — account 29

```
Limit  (cost=27.41..27.41 rows=1 width=55) (actual time=0.281..0.282 rows=0 loops=1)
  Buffers: shared hit=26
  ->  Sort  (cost=27.41..27.41 rows=1 width=55) (actual time=0.280..0.281 rows=0 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=26
        ->  Bitmap Heap Scan on messages  (cost=26.28..27.40 rows=1 width=55) (actual time=0.277..0.278 rows=0 loops=1)
              Recheck Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
              Filter: (account_id = 29)
              Rows Removed by Filter: 5
              Heap Blocks: exact=5
              Buffers: shared hit=26
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..26.28 rows=1 width=0) (actual time=0.024..0.025 rows=5 loops=1)
                    Index Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=2
Planning Time: 0.705 ms
Execution Time: 0.308 ms
```

#### Shape 5 — prefix (search-as-you-type) + date DESC — account 1

```
Limit  (cost=0.43..58.11 rows=50 width=55) (actual time=0.116..1.702 rows=50 loops=1)
  Buffers: shared hit=564
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..380773.48 rows=330116 width=55) (actual time=0.114..1.691 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factur'':*'::tsquery)
        Rows Removed by Filter: 117
        Buffers: shared hit=564
Planning:
  Buffers: shared hit=2
Planning Time: 0.884 ms
Execution Time: 1.737 ms
```

#### Shape 5 — prefix (search-as-you-type) + date DESC — account 29

```
Limit  (cost=0.43..87.42 rows=50 width=55) (actual time=0.087..1.188 rows=50 loops=1)
  Buffers: shared hit=529
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..15328.19 rows=8810 width=55) (actual time=0.086..1.177 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''factur'':*'::tsquery)
        Rows Removed by Filter: 114
        Buffers: shared hit=529
Planning:
  Buffers: shared hit=2
Planning Time: 1.115 ms
Execution Time: 1.224 ms
```

#### Shape 6 — common word + mailbox + last 90 days — account 1

```
Limit  (cost=0.44..222.37 rows=50 width=55) (actual time=0.310..4.087 rows=50 loops=1)
  Buffers: shared hit=1122
  ->  Index Scan using messages_acct_mbox_date on messages  (cost=0.44..49851.60 rows=11231 width=55) (actual time=0.308..4.075 rows=50 loops=1)
        Index Cond: ((account_id = 1) AND (mailbox_id = 1) AND (date >= (now() - '90 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 278
        Buffers: shared hit=1122
Planning:
  Buffers: shared hit=2
Planning Time: 0.957 ms
Execution Time: 4.150 ms
```

#### Shape 6 — common word + mailbox + last 90 days — account 29

```
Limit  (cost=0.44..233.61 rows=50 width=55) (actual time=0.222..2.818 rows=50 loops=1)
  Buffers: shared hit=1180
  ->  Index Scan using messages_acct_mbox_date on messages  (cost=0.44..1399.46 rows=300 width=55) (actual time=0.221..2.804 rows=50 loops=1)
        Index Cond: ((account_id = 29) AND (mailbox_id = 1) AND (date >= (now() - '90 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 317
        Buffers: shared hit=1180
Planning:
  Buffers: shared hit=2
Planning Time: 1.034 ms
Execution Time: 2.863 ms
```

#### Shape 7 — common word + unread only — account 1

```
Limit  (cost=0.43..30617.42 rows=50 width=55) (actual time=0.370..13.806 rows=50 loops=1)
  Buffers: shared hit=5252
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..385774.52 rows=630 width=55) (actual time=0.369..13.791 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: ((tsv @@ '''factura'''::tsquery) AND ((flags & 1) = 0))
        Rows Removed by Filter: 1549
        Buffers: shared hit=5252
Planning:
  Buffers: shared hit=2
Planning Time: 0.746 ms
Execution Time: 13.852 ms
```

#### Shape 7 — common word + unread only — account 29

```
Limit  (cost=3769.55..3769.60 rows=17 width=55) (actual time=6.965..7.087 rows=50 loops=1)
  Buffers: shared hit=2247
  ->  Sort  (cost=3769.55..3769.60 rows=17 width=55) (actual time=6.963..7.079 rows=50 loops=1)
        Sort Key: date DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=2247
        ->  Bitmap Heap Scan on messages  (cost=35.97..3769.21 rows=17 width=55) (actual time=1.904..6.613 rows=975 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
              Filter: ((flags & 1) = 0)
              Rows Removed by Filter: 2354
              Heap Blocks: exact=2211
              Buffers: shared hit=2247
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..35.97 rows=3362 width=0) (actual time=1.556..1.557 rows=3329 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
                    Buffers: shared hit=36
Planning:
  Buffers: shared hit=2
Planning Time: 0.806 ms
Execution Time: 7.147 ms
```

#### Shape 8 — from-address search (weight B) — account 1

```
Limit  (cost=0.43..370.42 rows=50 width=55) (actual time=0.243..5.782 rows=50 loops=1)
  Buffers: shared hit=2496
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..380773.48 rows=51457 width=55) (actual time=0.242..5.768 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''samantha'''::tsquery)
        Rows Removed by Filter: 686
        Buffers: shared hit=2496
Planning:
  Buffers: shared hit=2
Planning Time: 0.764 ms
Execution Time: 5.827 ms
```

#### Shape 8 — from-address search (weight B) — account 29

```
Limit  (cost=0.43..558.62 rows=50 width=55) (actual time=0.079..7.037 rows=50 loops=1)
  Buffers: shared hit=3580
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..15328.19 rows=1373 width=55) (actual time=0.078..7.021 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''samantha'''::tsquery)
        Rows Removed by Filter: 1045
        Buffers: shared hit=3580
Planning:
  Buffers: shared hit=2
Planning Time: 0.737 ms
Execution Time: 7.080 ms
```

#### Shape 9 — two-word AND + ts_rank_cd relevance — account 1

```
Limit  (cost=16988.35..16988.48 rows=50 width=59) (actual time=670.500..670.518 rows=50 loops=1)
  Buffers: shared hit=203023
  ->  Sort  (cost=16988.35..17025.88 rows=15010 width=59) (actual time=670.498..670.509 rows=50 loops=1)
        Sort Key: (ts_rank_cd(tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 35kB
        Buffers: shared hit=203023
        ->  Bitmap Heap Scan on messages  (cost=111.22..16489.73 rows=15010 width=59) (actual time=55.134..655.214 rows=34814 loops=1)
              Recheck Cond: ((account_id = 1) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
              Heap Blocks: exact=35079
              Buffers: shared hit=203023
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..107.47 rows=15010 width=0) (actual time=45.667..45.667 rows=40814 loops=1)
                    Index Cond: ((account_id = 1) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                    Buffers: shared hit=415
Planning:
  Buffers: shared hit=2
Planning Time: 0.741 ms
Execution Time: 670.577 ms
```

#### Shape 9 — two-word AND + ts_rank_cd relevance — account 29

```
Limit  (cost=488.86..488.99 rows=50 width=59) (actual time=18.781..18.793 rows=50 loops=1)
  Buffers: shared hit=5213
  ->  Sort  (cost=488.86..489.87 rows=401 width=59) (actual time=18.779..18.785 rows=50 loops=1)
        Sort Key: (ts_rank_cd(tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=5213
        ->  Bitmap Heap Scan on messages  (cost=28.39..475.54 rows=401 width=59) (actual time=1.626..18.372 rows=895 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
              Heap Blocks: exact=812
              Buffers: shared hit=5213
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..28.29 rows=401 width=0) (actual time=1.455..1.456 rows=895 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                    Buffers: shared hit=60
Planning:
  Buffers: shared hit=2
Planning Time: 0.730 ms
Execution Time: 18.843 ms
```

#### Shape 10 — exact count for common word — account 1

```
Aggregate  (cost=125183.11..125183.12 rows=1 width=8) (actual time=359.607..359.609 rows=1 loops=1)
  Buffers: shared hit=86061
  ->  Bitmap Heap Scan on messages  (cost=735.71..124868.15 rows=125985 width=0) (actual time=97.912..349.540 rows=125880 loops=1)
        Recheck Cond: ((account_id = 1) AND (tsv @@ '''factura'''::tsquery))
        Heap Blocks: exact=85709
        Buffers: shared hit=86061
        ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..704.21 rows=125985 width=0) (actual time=64.419..64.420 rows=131880 loops=1)
              Index Cond: ((account_id = 1) AND (tsv @@ '''factura'''::tsquery))
              Buffers: shared hit=352
Planning:
  Buffers: shared hit=2
Planning Time: 0.770 ms
JIT:
  Functions: 5
  Options: Inlining false, Optimization false, Expressions true, Deforming true
  Timing: Generation 0.552 ms (Deform 0.210 ms), Inlining 0.000 ms, Optimization 0.474 ms, Emission 4.443 ms, Total 5.469 ms
Execution Time: 360.331 ms
```

#### Shape 10 — exact count for common word — account 29

```
Aggregate  (cost=3761.64..3761.65 rows=1 width=8) (actual time=8.303..8.306 rows=1 loops=1)
  Buffers: shared hit=2247
  ->  Bitmap Heap Scan on messages  (cost=36.81..3753.23 rows=3362 width=0) (actual time=1.778..8.012 rows=3329 loops=1)
        Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
        Heap Blocks: exact=2211
        Buffers: shared hit=2247
        ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..35.97 rows=3362 width=0) (actual time=1.452..1.453 rows=3329 loops=1)
              Index Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
              Buffers: shared hit=36
Planning:
  Buffers: shared hit=2
Planning Time: 0.726 ms
Execution Time: 8.387 ms
```

#### Shape 101 — MITIGATION #9: rank over top-500-by-date candidates — account 1

```
Limit  (cost=12702.27..12702.40 rows=50 width=59) (actual time=126.386..126.400 rows=50 loops=1)
  Buffers: shared hit=46013
  ->  Sort  (cost=12702.27..12703.52 rows=500 width=59) (actual time=126.384..126.392 rows=50 loops=1)
        Sort Key: (ts_rank_cd(messages.tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=46013
        ->  Limit  (cost=0.43..12685.66 rows=500 width=59) (actual time=0.993..125.829 rows=500 loops=1)
              Buffers: shared hit=46013
              ->  Index Scan using messages_acct_date on messages  (cost=0.43..380811.00 rows=15010 width=59) (actual time=0.992..125.649 rows=500 loops=1)
                    Index Cond: (account_id = 1)
                    Filter: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
                    Rows Removed by Filter: 12905
                    Buffers: shared hit=46013
Planning:
  Buffers: shared hit=2
Planning Time: 2.110 ms
Execution Time: 126.468 ms
```

#### Shape 101 — MITIGATION #9: rank over top-500-by-date candidates — account 29

```
Limit  (cost=507.20..507.33 rows=50 width=59) (actual time=20.848..20.859 rows=50 loops=1)
  Buffers: shared hit=5213
  ->  Sort  (cost=507.20..508.21 rows=401 width=59) (actual time=20.847..20.852 rows=50 loops=1)
        Sort Key: (ts_rank_cd(messages.tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=5213
        ->  Limit  (cost=492.88..493.88 rows=401 width=59) (actual time=20.553..20.720 rows=500 loops=1)
              Buffers: shared hit=5213
              ->  Sort  (cost=492.88..493.88 rows=401 width=59) (actual time=20.552..20.670 rows=500 loops=1)
                    Sort Key: messages.date DESC
                    Sort Method: quicksort  Memory: 126kB
                    Buffers: shared hit=5213
                    ->  Bitmap Heap Scan on messages  (cost=28.39..475.54 rows=401 width=59) (actual time=2.685..19.890 rows=895 loops=1)
                          Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                          Heap Blocks: exact=812
                          Buffers: shared hit=5213
                          ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..28.29 rows=401 width=0) (actual time=2.466..2.467 rows=895 loops=1)
                                Index Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                                Buffers: shared hit=60
Planning:
  Buffers: shared hit=2
Planning Time: 0.915 ms
Execution Time: 20.927 ms
```

#### Shape 102 — MITIGATION #9: rank over top-200-by-date candidates — account 1

```
Limit  (cost=5081.17..5081.29 rows=50 width=59) (actual time=46.372..46.386 rows=50 loops=1)
  Buffers: shared hit=18446
  ->  Sort  (cost=5081.17..5081.67 rows=200 width=59) (actual time=46.371..46.378 rows=50 loops=1)
        Sort Key: (ts_rank_cd(messages.tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=18446
        ->  Limit  (cost=0.43..5074.52 rows=200 width=59) (actual time=0.987..46.183 rows=200 loops=1)
              Buffers: shared hit=18446
              ->  Index Scan using messages_acct_date on messages  (cost=0.43..380811.00 rows=15010 width=59) (actual time=0.987..46.124 rows=200 loops=1)
                    Index Cond: (account_id = 1)
                    Filter: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
                    Rows Removed by Filter: 5133
                    Buffers: shared hit=18446
Planning:
  Buffers: shared hit=2
Planning Time: 1.088 ms
Execution Time: 46.435 ms
```

#### Shape 102 — MITIGATION #9: rank over top-200-by-date candidates — account 29

```
Limit  (cost=500.02..500.14 rows=50 width=59) (actual time=19.124..19.137 rows=50 loops=1)
  Buffers: shared hit=5213
  ->  Sort  (cost=500.02..500.52 rows=200 width=59) (actual time=19.123..19.129 rows=50 loops=1)
        Sort Key: (ts_rank_cd(messages.tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 33kB
        Buffers: shared hit=5213
        ->  Limit  (cost=492.87..493.37 rows=200 width=59) (actual time=18.988..19.040 rows=200 loops=1)
              Buffers: shared hit=5213
              ->  Sort  (cost=492.87..493.88 rows=401 width=59) (actual time=18.987..19.016 rows=200 loops=1)
                    Sort Key: messages.date DESC
                    Sort Method: top-N heapsort  Memory: 63kB
                    Buffers: shared hit=5213
                    ->  Bitmap Heap Scan on messages  (cost=28.39..475.54 rows=401 width=59) (actual time=2.155..18.507 rows=895 loops=1)
                          Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                          Heap Blocks: exact=812
                          Buffers: shared hit=5213
                          ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..28.29 rows=401 width=0) (actual time=1.920..1.921 rows=895 loops=1)
                                Index Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
                                Buffers: shared hit=60
Planning:
  Buffers: shared hit=2
Planning Time: 0.837 ms
Execution Time: 19.210 ms
```

#### Shape 103 — MITIGATION #10: capped count (LIMIT 1000 — '999+') — account 1

```
Aggregate  (cost=1733.50..1733.51 rows=1 width=8) (actual time=90.562..90.565 rows=1 loops=1)
  Buffers: shared hit=1121
  ->  Limit  (cost=735.71..1721.00 rows=1000 width=4) (actual time=87.044..90.466 rows=1000 loops=1)
        Buffers: shared hit=1121
        ->  Bitmap Heap Scan on messages  (cost=735.71..124868.15 rows=125985 width=4) (actual time=87.041..90.363 rows=1000 loops=1)
              Recheck Cond: ((account_id = 1) AND (tsv @@ '''factura'''::tsquery))
              Heap Blocks: exact=769
              Buffers: shared hit=1121
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..704.21 rows=125985 width=0) (actual time=57.738..57.739 rows=131880 loops=1)
                    Index Cond: ((account_id = 1) AND (tsv @@ '''factura'''::tsquery))
                    Buffers: shared hit=352
Planning:
  Buffers: shared hit=2
Planning Time: 0.618 ms
Execution Time: 90.629 ms
```

#### Shape 103 — MITIGATION #10: capped count (LIMIT 1000 — '999+') — account 29

```
Aggregate  (cost=1154.73..1154.74 rows=1 width=8) (actual time=6.314..6.318 rows=1 loops=1)
  Buffers: shared hit=686
  ->  Limit  (cost=36.81..1142.23 rows=1000 width=4) (actual time=4.362..6.217 rows=1000 loops=1)
        Buffers: shared hit=686
        ->  Bitmap Heap Scan on messages  (cost=36.81..3753.23 rows=3362 width=4) (actual time=4.359..6.087 rows=1000 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
              Heap Blocks: exact=650
              Buffers: shared hit=686
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..35.97 rows=3362 width=0) (actual time=2.217..2.218 rows=3329 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
                    Buffers: shared hit=36
Planning:
  Buffers: shared hit=2
Planning Time: 0.728 ms
Execution Time: 6.389 ms
```

#### Shape 104 — MITIGATION #10: capped count (LIMIT 200 — '199+') — account 1

```
Aggregate  (cost=607.41..607.42 rows=1 width=8) (actual time=13.421..13.424 rows=1 loops=1)
  Buffers: shared hit=5053
  ->  Limit  (cost=0.43..604.91 rows=200 width=4) (actual time=0.211..13.371 rows=200 loops=1)
        Buffers: shared hit=5053
        ->  Index Scan using messages_acct_date on messages  (cost=0.43..380773.48 rows=125985 width=4) (actual time=0.209..13.328 rows=200 loops=1)
              Index Cond: (account_id = 1)
              Filter: (tsv @@ '''factura'''::tsquery)
              Rows Removed by Filter: 1345
              Buffers: shared hit=5053
Planning:
  Buffers: shared hit=2
Planning Time: 0.790 ms
Execution Time: 13.475 ms
```

#### Shape 104 — MITIGATION #10: capped count (LIMIT 200 — '199+') — account 29

```
Aggregate  (cost=260.39..260.40 rows=1 width=8) (actual time=2.252..2.254 rows=1 loops=1)
  Buffers: shared hit=173
  ->  Limit  (cost=36.81..257.89 rows=200 width=4) (actual time=1.779..2.231 rows=200 loops=1)
        Buffers: shared hit=173
        ->  Bitmap Heap Scan on messages  (cost=36.81..3753.23 rows=3362 width=4) (actual time=1.778..2.209 rows=200 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
              Heap Blocks: exact=137
              Buffers: shared hit=173
              ->  Bitmap Index Scan on messages_acct_tsv_gin  (cost=0.00..35.97 rows=3362 width=0) (actual time=1.319..1.320 rows=3329 loops=1)
                    Index Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
                    Buffers: shared hit=36
Planning:
  Buffers: shared hit=2
Planning Time: 0.741 ms
Execution Time: 2.296 ms
```

__COMPLETE__
