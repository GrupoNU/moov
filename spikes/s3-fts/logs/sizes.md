<!-- median account = 29, frequent sender = samantha -->

| Object | Size |
|---|---:|
| table (heap+toast, no indexes) | 18 GB |
| heap only | 5379 MB |
| toast | 12 GB |
| GIN index on tsv | 2507 MB |
| btree (account_id,date) | 150 MB |
| btree (account_id,mailbox_id,date) | 150 MB |
| TOTAL with indexes | 23 GB |

- Mean `pg_column_size(tsv)`: **2192 bytes** → ~11.0 GB over 5M rows
- Mean `pg_column_size(body_text)`: **1086 bytes** → ~5.4 GB over 5M rows

