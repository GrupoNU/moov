<!-- median account = 29, frequent sender = samantha -->

#### Shape 1 — common word + date DESC — account 1

```
Limit  (cost=0.43..152.33 rows=50 width=55) (actual time=0.179..4.647 rows=50 loops=1)
  Buffers: shared hit=1218
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=124835 width=55) (actual time=0.177..4.632 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 328
        Buffers: shared hit=1218
Planning:
  Buffers: shared hit=1
Planning Time: 0.290 ms
Execution Time: 4.710 ms
```

#### Shape 1 — common word + date DESC — account 29

```
Limit  (cost=0.43..229.38 rows=50 width=55) (actual time=0.202..3.573 rows=50 loops=1)
  Buffers: shared hit=1369
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..14268.25 rows=3116 width=55) (actual time=0.201..3.563 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 385
        Buffers: shared hit=1369
Planning:
  Buffers: shared hit=1
Planning Time: 0.327 ms
Execution Time: 3.612 ms
```

#### Shape 2 — rare word + date DESC — account 1

```
Limit  (cost=0.43..3815.87 rows=50 width=55) (actual time=157.085..13084.841 rows=10 loops=1)
  Buffers: shared hit=3238837
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=4970 width=55) (actual time=157.082..13084.807 rows=10 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''zanzibarita'''::tsquery)
        Rows Removed by Filter: 999990
        Buffers: shared hit=3238837
Planning:
  Buffers: shared hit=1
Planning Time: 0.595 ms
Execution Time: 13084.975 ms
```

#### Shape 2 — rare word + date DESC — account 29

```
Limit  (cost=580.12..580.24 rows=50 width=55) (actual time=2.705..2.710 rows=0 loops=1)
  Buffers: shared hit=111
  ->  Sort  (cost=580.12..580.43 rows=124 width=55) (actual time=2.701..2.705 rows=0 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=111
        ->  Bitmap Heap Scan on messages  (cost=437.91..576.00 rows=124 width=55) (actual time=2.688..2.691 rows=0 loops=1)
              Recheck Cond: ((tsv @@ '''zanzibarita'''::tsquery) AND (account_id = 29))
              Buffers: shared hit=111
              ->  BitmapAnd  (cost=437.91..437.91 rows=124 width=0) (actual time=2.661..2.664 rows=0 loops=1)
                    Buffers: shared hit=111
                    ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..145.49 rows=24977 width=0) (actual time=0.054..0.054 rows=37 loops=1)
                          Index Cond: (tsv @@ '''zanzibarita'''::tsquery)
                          Buffers: shared hit=5
                    ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=2.593..2.593 rows=26640 loops=1)
                          Index Cond: (account_id = 29)
                          Buffers: shared hit=106
Planning:
  Buffers: shared hit=1
Planning Time: 0.619 ms
Execution Time: 2.805 ms
```

#### Shape 3 — two-word AND + date DESC — account 1

```
Limit  (cost=0.43..1297.65 rows=50 width=55) (actual time=1.411..12.886 rows=50 loops=1)
  Buffers: shared hit=3747
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=14618 width=55) (actual time=1.409..12.871 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
        Rows Removed by Filter: 1104
        Buffers: shared hit=3747
Planning:
  Buffers: shared hit=1
Planning Time: 0.423 ms
Execution Time: 12.988 ms
```

#### Shape 3 — two-word AND + date DESC — account 29

```
Limit  (cost=1127.93..1128.06 rows=50 width=55) (actual time=204.498..204.513 rows=50 loops=1)
  Buffers: shared hit=1312
  ->  Sort  (cost=1127.93..1128.84 rows=365 width=55) (actual time=204.495..204.503 rows=50 loops=1)
        Sort Key: date DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=1312
        ->  Bitmap Heap Scan on messages  (cost=709.67..1115.81 rows=365 width=55) (actual time=202.163..204.253 rows=895 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
              Heap Blocks: exact=812
              Buffers: shared hit=1312
              ->  BitmapAnd  (cost=709.67..709.67 rows=365 width=0) (actual time=201.947..201.950 rows=0 loops=1)
                    Buffers: shared hit=500
                    ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=4.674..4.675 rows=26640 loops=1)
                          Index Cond: (account_id = 29)
                          Buffers: shared hit=106
                    ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..417.13 rows=73470 width=0) (actual time=195.602..195.603 rows=173195 loops=1)
                          Index Cond: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
                          Buffers: shared hit=394
Planning:
  Buffers: shared hit=1
Planning Time: 0.454 ms
Execution Time: 211.614 ms
```

#### Shape 4 — phrase + date DESC — account 1

```
Limit  (cost=27.41..27.42 rows=1 width=55) (actual time=0.784..0.786 rows=1 loops=1)
  Buffers: shared hit=26
  ->  Sort  (cost=27.41..27.42 rows=1 width=55) (actual time=0.782..0.783 rows=1 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=26
        ->  Bitmap Heap Scan on messages  (cost=26.29..27.40 rows=1 width=55) (actual time=0.113..0.769 rows=1 loops=1)
              Recheck Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
              Filter: (account_id = 1)
              Rows Removed by Filter: 4
              Heap Blocks: exact=5
              Buffers: shared hit=26
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..26.29 rows=1 width=0) (actual time=0.089..0.090 rows=5 loops=1)
                    Index Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=1
Planning Time: 0.552 ms
Execution Time: 0.877 ms
```

#### Shape 4 — phrase + date DESC — account 29

```
Limit  (cost=27.41..27.42 rows=1 width=55) (actual time=0.331..0.332 rows=0 loops=1)
  Buffers: shared hit=26
  ->  Sort  (cost=27.41..27.42 rows=1 width=55) (actual time=0.327..0.328 rows=0 loops=1)
        Sort Key: date DESC
        Sort Method: quicksort  Memory: 25kB
        Buffers: shared hit=26
        ->  Bitmap Heap Scan on messages  (cost=26.29..27.40 rows=1 width=55) (actual time=0.312..0.312 rows=0 loops=1)
              Recheck Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
              Filter: (account_id = 29)
              Rows Removed by Filter: 5
              Heap Blocks: exact=5
              Buffers: shared hit=26
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..26.29 rows=1 width=0) (actual time=0.089..0.090 rows=5 loops=1)
                    Index Cond: (tsv @@ '''quetzal'' <-> ''ferroviario'' <-> ''nocturno'''::tsquery)
                    Buffers: shared hit=13
Planning:
  Buffers: shared hit=1
Planning Time: 1.409 ms
Execution Time: 0.412 ms
```

#### Shape 5 — prefix (search-as-you-type) + date DESC — account 1

```
Limit  (cost=0.43..58.44 rows=50 width=55) (actual time=0.109..6.856 rows=50 loops=1)
  Buffers: shared hit=554
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=326926 width=55) (actual time=0.107..6.842 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''factur'':*'::tsquery)
        Rows Removed by Filter: 117
        Buffers: shared hit=554
Planning:
  Buffers: shared hit=1
Planning Time: 0.378 ms
Execution Time: 6.909 ms
```

#### Shape 5 — prefix (search-as-you-type) + date DESC — account 29

```
Limit  (cost=0.43..87.85 rows=50 width=55) (actual time=0.120..1.481 rows=50 loops=1)
  Buffers: shared hit=529
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..14268.25 rows=8161 width=55) (actual time=0.119..1.471 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''factur'':*'::tsquery)
        Rows Removed by Filter: 114
        Buffers: shared hit=529
Planning:
  Buffers: shared hit=1
Planning Time: 0.323 ms
Execution Time: 1.520 ms
```

#### Shape 6 — common word + mailbox + last 90 days — account 1

```
Limit  (cost=0.44..223.24 rows=50 width=55) (actual time=0.163..4.451 rows=50 loops=1)
  Buffers: shared hit=1112
  ->  Index Scan using messages_acct_mbox_date on messages  (cost=0.44..49200.61 rows=11041 width=55) (actual time=0.161..4.439 rows=50 loops=1)
        Index Cond: ((account_id = 1) AND (mailbox_id = 1) AND (date >= (now() - '90 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 278
        Buffers: shared hit=1112
Planning:
  Buffers: shared hit=1
Planning Time: 0.295 ms
Execution Time: 4.501 ms
```

#### Shape 6 — common word + mailbox + last 90 days — account 29

```
Limit  (cost=0.44..234.30 rows=50 width=55) (actual time=0.442..3.628 rows=50 loops=1)
  Buffers: shared hit=1180
  ->  Index Scan using messages_acct_mbox_date on messages  (cost=0.44..1291.33 rows=276 width=55) (actual time=0.441..3.616 rows=50 loops=1)
        Index Cond: ((account_id = 29) AND (mailbox_id = 1) AND (date >= (now() - '90 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 317
        Buffers: shared hit=1180
Planning:
  Buffers: shared hit=1
Planning Time: 0.603 ms
Execution Time: 3.682 ms
```

#### Shape 7 — common word + unread only — account 1

```
Limit  (cost=0.43..30787.62 rows=50 width=55) (actual time=0.475..18.189 rows=50 loops=1)
  Buffers: shared hit=5242
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..384224.57 rows=624 width=55) (actual time=0.473..18.162 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: ((tsv @@ '''factura'''::tsquery) AND ((flags & 1) = 0))
        Rows Removed by Filter: 1549
        Buffers: shared hit=5242
Planning:
  Buffers: shared hit=1
Planning Time: 0.385 ms
Execution Time: 18.255 ms
```

#### Shape 7 — common word + unread only — account 29

```
Limit  (cost=7181.19..7181.23 rows=16 width=55) (actual time=469.021..469.036 rows=50 loops=1)
  Buffers: shared hit=2459
  ->  Sort  (cost=7181.19..7181.23 rows=16 width=55) (actual time=469.018..469.027 rows=50 loops=1)
        Sort Key: date DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=2459
        ->  Bitmap Heap Scan on messages  (cost=3719.54..7180.87 rows=16 width=55) (actual time=463.272..468.711 rows=975 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
              Filter: ((flags & 1) = 0)
              Rows Removed by Filter: 2354
              Heap Blocks: exact=2211
              Buffers: shared hit=2459
              ->  BitmapAnd  (cost=3719.54..3719.54 rows=3116 width=0) (actual time=462.820..462.823 rows=0 loops=1)
                    Buffers: shared hit=248
                    ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=3.412..3.413 rows=26640 loops=1)
                          Index Cond: (account_id = 29)
                          Buffers: shared hit=106
                    ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..3427.18 rows=627415 width=0) (actual time=448.828..448.828 rows=628581 loops=1)
                          Index Cond: (tsv @@ '''factura'''::tsquery)
                          Buffers: shared hit=142
Planning:
  Buffers: shared hit=1
Planning Time: 0.414 ms
Execution Time: 478.432 ms
```

#### Shape 8 — from-address search (weight B) — account 1

```
Limit  (cost=0.43..365.00 rows=50 width=55) (actual time=0.341..31.375 rows=50 loops=1)
  Buffers: shared hit=2486
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=52014 width=55) (actual time=0.339..31.348 rows=50 loops=1)
        Index Cond: (account_id = 1)
        Filter: (tsv @@ '''samantha'''::tsquery)
        Rows Removed by Filter: 686
        Buffers: shared hit=2486
Planning:
  Buffers: shared hit=1
Planning Time: 0.423 ms
Execution Time: 31.444 ms
```

#### Shape 8 — from-address search (weight B) — account 29

```
Limit  (cost=0.43..550.04 rows=50 width=55) (actual time=0.184..19.194 rows=50 loops=1)
  Buffers: shared hit=3580
  ->  Index Scan using messages_acct_date on messages  (cost=0.43..14268.25 rows=1298 width=55) (actual time=0.182..19.169 rows=50 loops=1)
        Index Cond: (account_id = 29)
        Filter: (tsv @@ '''samantha'''::tsquery)
        Rows Removed by Filter: 1045
        Buffers: shared hit=3580
Planning:
  Buffers: shared hit=1
Planning Time: 0.363 ms
Execution Time: 19.249 ms
```

#### Shape 9 — two-word AND + ts_rank_cd relevance — account 1

```
Limit  (cost=28532.38..28532.51 rows=50 width=59) (actual time=1554.920..1554.950 rows=50 loops=1)
  Buffers: shared hit=202255
  ->  Sort  (cost=28532.38..28568.93 rows=14618 width=59) (actual time=1554.916..1554.930 rows=50 loops=1)
        Sort Key: (ts_rank_cd(tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 35kB
        Buffers: shared hit=202255
        ->  Bitmap Heap Scan on messages  (cost=12089.13..28046.78 rows=14618 width=59) (actual time=474.294..1534.917 rows=34814 loops=1)
              Recheck Cond: ((tsv @@ '''factura'' & ''vencimiento'''::tsquery) AND (account_id = 1))
              Heap Blocks: exact=30511
              Buffers: shared hit=202255
              ->  BitmapAnd  (cost=12089.13..12089.13 rows=14618 width=0) (actual time=459.376..459.378 rows=0 loops=1)
                    Buffers: shared hit=4215
                    ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..417.13 rows=73470 width=0) (actual time=155.776..155.776 rows=173195 loops=1)
                          Index Cond: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
                          Buffers: shared hit=394
                    ->  Bitmap Index Scan on messages_acct_date  (cost=0.00..11664.44 rows=993907 width=0) (actual time=295.261..295.261 rows=1000000 loops=1)
                          Index Cond: (account_id = 1)
                          Buffers: shared hit=3821
Planning:
  Buffers: shared hit=1
Planning Time: 0.300 ms
Execution Time: 1555.021 ms
```

#### Shape 9 — two-word AND + ts_rank_cd relevance — account 29

```
Limit  (cost=1128.84..1128.97 rows=50 width=59) (actual time=184.938..184.952 rows=50 loops=1)
  Buffers: shared hit=5653
  ->  Sort  (cost=1128.84..1129.76 rows=365 width=59) (actual time=184.935..184.943 rows=50 loops=1)
        Sort Key: (ts_rank_cd(tsv, '''factura'' & ''vencimiento'''::tsquery)) DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=5653
        ->  Bitmap Heap Scan on messages  (cost=709.67..1116.72 rows=365 width=59) (actual time=168.435..184.531 rows=895 loops=1)
              Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'' & ''vencimiento'''::tsquery))
              Heap Blocks: exact=812
              Buffers: shared hit=5653
              ->  BitmapAnd  (cost=709.67..709.67 rows=365 width=0) (actual time=168.137..168.139 rows=0 loops=1)
                    Buffers: shared hit=500
                    ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=4.428..4.428 rows=26640 loops=1)
                          Index Cond: (account_id = 29)
                          Buffers: shared hit=106
                    ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..417.13 rows=73470 width=0) (actual time=161.964..161.964 rows=173195 loops=1)
                          Index Cond: (tsv @@ '''factura'' & ''vencimiento'''::tsquery)
                          Buffers: shared hit=394
Planning:
  Buffers: shared hit=1
Planning Time: 1.013 ms
Execution Time: 185.033 ms
```

#### Shape 10 — exact count for common word — account 1

```
Aggregate  (cost=138578.19..138578.20 rows=1 width=8) (actual time=1178.337..1178.350 rows=1 loops=1)
  Buffers: shared hit=87254
  ->  Bitmap Heap Scan on messages  (cost=15154.28..138266.11 rows=124835 width=0) (actual time=822.813..1167.784 rows=125880 loops=1)
        Recheck Cond: ((tsv @@ '''factura'''::tsquery) AND (account_id = 1))
        Heap Blocks: exact=83291
        Buffers: shared hit=87254
        ->  BitmapAnd  (cost=15154.28..15154.28 rows=124835 width=0) (actual time=733.040..733.042 rows=0 loops=1)
              Buffers: shared hit=3963
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..3427.18 rows=627415 width=0) (actual time=338.773..338.774 rows=628581 loops=1)
                    Index Cond: (tsv @@ '''factura'''::tsquery)
                    Buffers: shared hit=142
              ->  Bitmap Index Scan on messages_acct_date  (cost=0.00..11664.44 rows=993907 width=0) (actual time=326.315..326.315 rows=1000000 loops=1)
                    Index Cond: (account_id = 1)
                    Buffers: shared hit=3821
Planning:
  Buffers: shared hit=1
Planning Time: 0.504 ms
JIT:
  Functions: 5
  Options: Inlining false, Optimization false, Expressions true, Deforming true
  Timing: Generation 1.582 ms (Deform 0.390 ms), Inlining 0.000 ms, Optimization 0.635 ms, Emission 5.304 ms, Total 7.521 ms
Execution Time: 1191.967 ms
```

#### Shape 10 — exact count for common word — account 29

```
Aggregate  (cost=7174.63..7174.64 rows=1 width=8) (actual time=382.755..382.760 rows=1 loops=1)
  Buffers: shared hit=2459
  ->  Bitmap Heap Scan on messages  (cost=3721.09..7166.84 rows=3116 width=0) (actual time=376.035..382.474 rows=3329 loops=1)
        Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
        Heap Blocks: exact=2211
        Buffers: shared hit=2459
        ->  BitmapAnd  (cost=3721.09..3721.09 rows=3116 width=0) (actual time=375.396..375.399 rows=0 loops=1)
              Buffers: shared hit=248
              ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=2.462..2.462 rows=26640 loops=1)
                    Index Cond: (account_id = 29)
                    Buffers: shared hit=106
              ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..3427.18 rows=627415 width=0) (actual time=360.358..360.358 rows=628581 loops=1)
                    Index Cond: (tsv @@ '''factura'''::tsquery)
                    Buffers: shared hit=142
Planning:
  Buffers: shared hit=1
Planning Time: 0.385 ms
Execution Time: 398.468 ms
```

#### Shape 101 — MITIGATION common word, two-phase (CTE match then sort) — account 1

```
Limit  (cost=144909.74..144909.86 rows=50 width=48) (actual time=1392.798..1392.822 rows=50 loops=1)
  Buffers: shared hit=87254
  CTE hits
    ->  Bitmap Heap Scan on messages  (cost=15154.28..138266.11 rows=124835 width=55) (actual time=774.826..1268.669 rows=125880 loops=1)
          Recheck Cond: ((tsv @@ '''factura'''::tsquery) AND (account_id = 1))
          Heap Blocks: exact=83291
          Buffers: shared hit=87254
          ->  BitmapAnd  (cost=15154.28..15154.28 rows=124835 width=0) (actual time=705.441..705.445 rows=0 loops=1)
                Buffers: shared hit=3963
                ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..3427.18 rows=627415 width=0) (actual time=339.456..339.457 rows=628581 loops=1)
                      Index Cond: (tsv @@ '''factura'''::tsquery)
                      Buffers: shared hit=142
                ->  Bitmap Index Scan on messages_acct_date  (cost=0.00..11664.44 rows=993907 width=0) (actual time=334.496..334.497 rows=1000000 loops=1)
                      Index Cond: (account_id = 1)
                      Buffers: shared hit=3821
  ->  Sort  (cost=6643.63..6955.72 rows=124835 width=48) (actual time=1382.228..1382.241 rows=50 loops=1)
        Sort Key: hits.date DESC
        Sort Method: top-N heapsort  Memory: 33kB
        Buffers: shared hit=87254
        ->  CTE Scan on hits  (cost=0.00..2496.70 rows=124835 width=48) (actual time=774.836..1352.237 rows=125880 loops=1)
              Buffers: shared hit=87254
Planning:
  Buffers: shared hit=1
Planning Time: 0.570 ms
JIT:
  Functions: 5
  Options: Inlining false, Optimization false, Expressions true, Deforming true
  Timing: Generation 1.722 ms (Deform 0.524 ms), Inlining 0.000 ms, Optimization 1.372 ms, Emission 9.236 ms, Total 12.329 ms
Execution Time: 1415.204 ms
```

#### Shape 101 — MITIGATION common word, two-phase (CTE match then sort) — account 29

```
Limit  (cost=7332.67..7332.80 rows=50 width=48) (actual time=400.329..400.349 rows=50 loops=1)
  Buffers: shared hit=2459
  CTE hits
    ->  Bitmap Heap Scan on messages  (cost=3721.09..7166.84 rows=3116 width=55) (actual time=390.701..398.036 rows=3329 loops=1)
          Recheck Cond: ((account_id = 29) AND (tsv @@ '''factura'''::tsquery))
          Heap Blocks: exact=2211
          Buffers: shared hit=2459
          ->  BitmapAnd  (cost=3721.09..3721.09 rows=3116 width=0) (actual time=390.161..390.165 rows=0 loops=1)
                Buffers: shared hit=248
                ->  Bitmap Index Scan on messages_acct_mbox_date  (cost=0.00..292.11 rows=24810 width=0) (actual time=2.331..2.331 rows=26640 loops=1)
                      Index Cond: (account_id = 29)
                      Buffers: shared hit=106
                ->  Bitmap Index Scan on messages_tsv_gin  (cost=0.00..3427.18 rows=627415 width=0) (actual time=376.786..376.786 rows=628581 loops=1)
                      Index Cond: (tsv @@ '''factura'''::tsquery)
                      Buffers: shared hit=142
  ->  Sort  (cost=165.83..173.62 rows=3116 width=48) (actual time=400.325..400.335 rows=50 loops=1)
        Sort Key: hits.date DESC
        Sort Method: top-N heapsort  Memory: 34kB
        Buffers: shared hit=2459
        ->  CTE Scan on hits  (cost=0.00..62.32 rows=3116 width=48) (actual time=390.709..399.797 rows=3329 loops=1)
              Buffers: shared hit=2459
Planning:
  Buffers: shared hit=1
Planning Time: 0.358 ms
Execution Time: 439.020 ms
```

#### Shape 102 — MITIGATION common word, date-bounded window (last 365d, fallback widen) — account 1

```
Limit  (cost=0.44..194.99 rows=50 width=55) (actual time=0.223..7.601 rows=50 loops=1)
  Buffers: shared hit=1218
  ->  Index Scan using messages_acct_date on messages  (cost=0.44..170445.64 rows=43805 width=55) (actual time=0.222..7.585 rows=50 loops=1)
        Index Cond: ((account_id = 1) AND (date >= (now() - '365 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 328
        Buffers: shared hit=1218
Planning:
  Buffers: shared hit=1
Planning Time: 0.624 ms
Execution Time: 7.664 ms
```

#### Shape 102 — MITIGATION common word, date-bounded window (last 365d, fallback widen) — account 29

```
Limit  (cost=0.44..232.71 rows=50 width=55) (actual time=0.451..5.664 rows=50 loops=1)
  Buffers: shared hit=1369
  ->  Index Scan using messages_acct_date on messages  (cost=0.44..5077.98 rows=1093 width=55) (actual time=0.450..5.649 rows=50 loops=1)
        Index Cond: ((account_id = 29) AND (date >= (now() - '365 days'::interval)))
        Filter: (tsv @@ '''factura'''::tsquery)
        Rows Removed by Filter: 385
        Buffers: shared hit=1369
Planning:
  Buffers: shared hit=1
Planning Time: 0.574 ms
Execution Time: 5.721 ms
```

#### Shape 103 — MITIGATION estimated count (rows from EXPLAIN-style estimate) — account 1

```
Aggregate  (cost=3050.98..3050.99 rows=1 width=8) (actual time=132.119..132.122 rows=1 loops=1)
  Buffers: shared hit=25021
  ->  Limit  (cost=0.43..3038.48 rows=1000 width=4) (actual time=0.225..131.862 rows=1000 loops=1)
        Buffers: shared hit=25021
        ->  Index Scan using messages_acct_date on messages  (cost=0.43..379255.04 rows=124835 width=4) (actual time=0.224..131.582 rows=1000 loops=1)
              Index Cond: (account_id = 1)
              Filter: (tsv @@ '''factura'''::tsquery)
              Rows Removed by Filter: 6654
              Buffers: shared hit=25021
Planning:
  Buffers: shared hit=1
Planning Time: 0.342 ms
Execution Time: 132.189 ms
```

#### Shape 103 — MITIGATION estimated count (rows from EXPLAIN-style estimate) — account 29

```
Aggregate  (cost=4591.82..4591.83 rows=1 width=8) (actual time=90.862..90.866 rows=1 loops=1)
  Buffers: shared hit=24863
  ->  Limit  (cost=0.43..4579.32 rows=1000 width=4) (actual time=0.338..90.577 rows=1000 loops=1)
        Buffers: shared hit=24863
        ->  Index Scan using messages_acct_mbox_date on messages  (cost=0.43..14268.25 rows=3116 width=4) (actual time=0.335..90.330 rows=1000 loops=1)
              Index Cond: (account_id = 29)
              Filter: (tsv @@ '''factura'''::tsquery)
              Rows Removed by Filter: 6681
              Buffers: shared hit=24863
Planning:
  Buffers: shared hit=1
Planning Time: 0.337 ms
Execution Time: 92.304 ms
```

__COMPLETE__
