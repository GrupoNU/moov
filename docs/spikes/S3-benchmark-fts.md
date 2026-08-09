# Spike S3 — Benchmark FTS: PostgreSQL tsvector+GIN con 5M de mensajes

> **Fecha:** 2026-08-08/09 · **Resultado: ✅ VALIDADO — tsvector+GIN alcanza la vara; Meilisearch queda fuera del MVP (opción de fase 2)**
> Objetivo: decidir con números si la búsqueda del MVP se hace con PostgreSQL FTS o exige Meilisearch. Vara: p95 ≤ 100 ms en caliente (regla 1, Gmail-class) con el shape real de query.
> Corrida en el VPS mail (hardware de despliegue real: 8 vCPU/23 GB, conviviendo con Mailcow productivo). Datos crudos, EXPLAIN plans y código: `spikes/s3-fts/` (RESULTS.md).

## Veredicto

**8 de 10 shapes pasan con margen de 4x-30x** (3,1-23,6 ms p95) tanto en la cuenta power-user de 1M de mensajes como en la mediana de 26,6k — incluyendo todo lo que el usuario siente: keyword, frase, prefijo (search-as-you-type), filtros de carpeta/fecha/no-leídos y remitente. Bajo concurrencia de 8 clientes: **607 qps a 44,4 ms p95**. Los 2 shapes que fallan (ranking por relevancia y count exacto) fallan por razones fundamentales de la operación, no de PostgreSQL, y tienen respuesta de producto aceptable (la misma que toma Gmail).

**PERO**: el schema naive del brief fallaba 8 de 10 shapes. El entregable real del spike son las **tres configuraciones obligatorias** que convierten el fracaso en margen holgado.

## Números titulares (p95 caliente, cuenta de 1M de mensajes)

| Shape | p95 | Vara |
|---|---:|:--:|
| Palabra común + `ORDER BY date DESC LIMIT 50` (el "killer" predicho) | **9,3 ms** | ✅ el shape interactivo más rápido |
| Palabra rara | **3,3 ms** (era **10.636 ms** antes del tuning) | ✅ |
| Peor shape que pasa (no-leídos) | **23,6 ms** | ✅ |
| Ranking `ts_rank_cd` sin acotar | 892 ms | ❌ fundamental (§H5) |
| `count(*)` exacto | 452 ms | ❌ fundamental (§H5) |

## Hallazgos de ingeniería

| # | Hallazgo | Implicancia para Moov |
|---|---|---|
| H1 | La patología predicha (GIN + orden por fecha) **no ocurrió**: el planner camina `(account_id, date DESC)` y corta a los 50 matches. Las fallas reales vinieron de otro lado (H2-H4) | Los EXPLAIN plans mandan; la intuición sobre GIN+ORDER BY no |
| H2 | **`gin (account_id, tsv)` compuesto (vía `btree_gin`), no `gin (tsv)`**: sin `account_id` en el índice, cada búsqueda escanea la posting list de TODO el corpus (el costo escala con la instalación, no con el buzón del usuario). Término raro: 10.636 ms → 1,6 ms (**6.600x**) | Obligatorio en el MVP. Solo el índice compuesto (el plano es redundante: 2,5 GB ahorrados) |
| H3 | **La trampa del plan genérico** (el hallazgo más traicionero): pgx prepara statements; a la **5ª ejecución** PG cambia a plan genérico, pierde la selectividad del tsquery y elige un `BitmapAnd` que materializa el 1M de filas de la cuenta. **1.868 ms vs 19 ms (145x), degradación silenciosa a mitad de sesión** — jamás aparecería en dev | `plan_cache_mode = force_custom_plan` en toda conexión de búsqueda. Verificar interacción con PgBouncer transaction-mode antes de adoptarlo |
| H4 | **Estadísticas default subestiman la selectividad del tsv ~500x** (10 filas estimadas como 4.951): el planner caminaba el índice de fecha filtrando 999.990 filas (13 s) | `ALTER COLUMN tsv SET STATISTICS 4000` + ANALYZE (~7 min, aceptable) |
| H5 | **Ranking y count exacto son insalvables por índice**: sin atajo de LIMIT deben tocar TODOS los matches (34.814 para el caso medido). Mitigaciones medidas: count capeado "199+" = **98 ms (pasa)**; rank sobre los 200 más recientes = 134 ms (1,3x sobre vara). Bajo carga, incluirlos lleva el peor caso de 0,7 s a **68 s** y parte el throughput a la mitad | Decisiones de producto forzadas: counts capeados ("199+"), relevancia **opt-in** acotada a ventana reciente (default = fecha, 9 ms), y **pool de conexiones separado con `statement_timeout`** para rank/count — un power user no puede degradar la búsqueda de todos |
| H6 | **El sync inicial es CPU-bound en `to_tsvector`, no en IMAP**: 2.063 filas/s. 5M = ~40 min de COPY + ~37 min de build del GIN | Buzón de 1M ≈ 8 min de COPY. Construir los índices GIN **después** del primer bulk sync, nunca antes |
| H7 | **Presupuesto de storage: ~4,6 KB/mensaje all-in** (23 GB para 5M). El tsv solo pesa ~11 GB — **2x los bodies comprimidos** — y el texto sintético probablemente lo subestima | Si el storage aprieta, la palanca más grande es dropear el peso C (cuerpo) del índice — al costo de no buscar en cuerpos |
| H8 | **Inserts incrementales baratos y sin sorpresas**: 0,25 ms/mensaje con `fastupdate=on` (default); el temido spike de flush de pending list **no se materializó** a tasas de mail sync. `fastupdate=off` duplica el costo sin beneficio medido | Dejar el default. Headroom de ~4.000 msg/s por conexión |
| H9 | **El write más frecuente del engine es el flag update y no es gratis**: cambiar un int reescribe la fila entera incluyendo el tsv de ~2,2 KB en ambos índices GIN. Batcheado: **0,58 ms/mensaje (2,3x un insert; 23x más barato que fila-a-fila)** | Batchear flags siempre. Evaluar en el diseño del engine separar `flags` a una tabla lateral (un read-mark no debería tocar el tsvector). Falta soak test de bloat GIN bajo churn sostenido |
| H10 | Config `'simple'` + unaccent = **sin stemming** (`factura` ≠ `facturas`) — elegido por buzones mixtos es/en. Afecta calidad percibida más que latencia | Evaluar dual-config español/inglés o diccionario custom antes del MVP; el cliente puede compensar con prefijos mientras tanto |
| H11 | Frío: solo la primera query post-restart paga (hasta 775 ms); p50 vuelve a niveles calientes en la 2ª-3ª | Warm-up query al arrancar el servicio |

## Método (resumen)

- Corpus determinístico de 5M mensajes / 89 cuentas imitando el caso Crash + power users (1M/500K/250K), texto mixto es/en zipfiano, fechas cargadas a lo reciente. **Gate de corrección**: 6 agujas plantadas con conteos exactos conocidos, verificadas antes de medir nada.
- Latencias desde el driver Go (pgx), wall-clock con drain de filas (lo que paga la capa API), n=50 caliente + pasada en frío real (restart + `drop_caches`), percentiles nearest-rank.
- PG 17 en contenedor aislado (sin red de Mailcow, 6 CPUs/12 GB), disco vigilado durante toda la corrida, limpieza total al cierre (140 GB libres, idéntico al inicio; Mailcow/Postal intactos).

## Riesgos abiertos

1. Sin soak test de bloat GIN bajo churn de flags sostenido (días) — hacerlo antes de producción (H9).
2. PgBouncer transaction-mode vs `force_custom_plan`/prepared statements — verificar antes de adoptar pooler (H3).
3. Texto sintético: el mail real (replies citados, firmas, HTML-a-texto) infla el tsv — el 11 GB es piso, no techo (H7).
4. Stemming/calidad de recall no evaluados (H10).

## Estado de los spikes

Con S3 cerrado, **los 4 spikes bloqueantes están completos** (S1 JMAP, S2 go-imap/QRESYNC/NOTIFY, S3 FTS, S4 corpus MIME). Fase de spikes terminada → sigue diseño e implementación del producto.
