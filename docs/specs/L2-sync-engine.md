# Spec L2 — Sync Engine de Moov Mail (fase 1)

> **Estado:** ACEPTADA — arbitrajes A5/A6/A7 y orden de épicas delegados por Diego al director técnico (2026-08-09) y firmados bajo la regla 2 del proyecto · **Fecha:** 2026-08-09
> **Autor:** Fable 5 (director técnico) · **Nivel:** L2 (brief estructurado con contracts y ACs)
> **Base:** ADR-001 + hallazgos de los 4 spikes (S1 H1-H7 · S2 H1-H9 · S3 H1-H11 · S4 H1-H9)
> **Alcance:** el sync engine completo (IMAP → store) y sus fundaciones de repo/CI. La capa JMAP y la PWA tendrán sus propias L2 contra los contracts definidos acá.

---

## 1. Objetivo

Construir el corazón de Moov: un servicio Go que sincroniza buzones Dovecot vía IMAP a un store PostgreSQL propio (metadata + FTS + blobs content-addressed), manteniendo la invariante del ADR — **Dovecot es la fuente de verdad; Moov es cache reconstruible** — con la performance que exige la regla 1 (deltas en <1 s vía push real, búsqueda <100 ms).

**No-scope de esta L2:** servidor JMAP (L2 propia, consume los contracts de §4), PWA, undo send, proxy de imágenes, sanitización HTML (el pipeline la deja preparada: ver E4).

## 2. Decisiones de diseño (con su evidencia)

### 2.1 Topología de módulos

```
cmd/moovd/            — daemon principal (config, lifecycle, señales)
internal/imap/        — ÚNICO paquete que importa go-imap (regla ADR). Interfaz propia.
internal/parser/      — cascada MIME (S4). Sin dependencias de imap ni store.
internal/store/       — PostgreSQL: schema, queries, migraciones. Interfaz propia.
internal/blob/        — blobs content-addressed sha256, refcount, GC.
internal/sync/        — orquestación: initial sync, incremental, watcher, reconciler.
internal/index/       — interfaz Indexer (tsvector hoy, Meilisearch fase 2) + reindex.
internal/crypto/      — AES-256-GCM para app passwords (master key fuera de DB).
vendor/               — go-imap/v2 vendorizado con patch set (ver 2.2)
patches/              — los .patch aplicados sobre el pin, con procedencia y estado upstream
```

### 2.2 Dependencia go-imap: pin + patch set (S2)

- Pin **exacto por commit** de la rama `v2` (`@v2` no resuelve — S2 H2). Vendorizado.
- Patch set inicial, en `patches/` con README de re-validación por bump:
  1. **PR #757** (QRESYNC cliente completo) — validado end-to-end en S2 H3.
  2. **Fix encoder NOTIFY** (los 2 bugs de S2 H4: `STATUS` como atom, keyword `MAILBOXES`) — con upstream issue/PR nuestro.
  3. **Exponer `[MODIFIED]`** en `Store()` (S2 H6) — con upstream PR nuestro.
- CI corre los tests del vendor parcheado. Bump de pin = re-aplicar patches + re-correr la suite S2 (`spikes/s2-goimap` se promueve a test de integración opcional contra Dovecot real).

### 2.3 Modelo de datos (S3)

Schema base = el validado en S3 (`spikes/s3-fts/schema.sql` + `indexes.sql`) con una evolución estructural:

**A5 — Arbitraje: el estado volátil vive en tabla lateral.** S3 H9 midió que cambiar un flag reescribe la fila entera incluyendo ~2,2 KB de tsv en dos índices GIN, y que el churn de flags domina las escrituras de un buzón establecido. Por lo tanto:

- `messages` — inmutable post-parse: identidad, headers, fechas, estructura MIME, `tsv` (índice compuesto `gin(account_id, tsv)` vía `btree_gin` — S3 H2), referencias a blobs, `parse_status`.
- `message_state` — fila angosta y caliente: `(message_id PK, account_id, mailbox_id, uid, flags, keywords, modseq_seen, updated_at)`. Todos los flag updates y moves pegan acá; el tsv jamás se reescribe.
- `mailboxes` — incluye `uidvalidity`, `highestmodseq` local, roles SPECIAL-USE (S1 H validó el mapeo), estado de backfill por fases.
- `sync_log` — por cuenta: checkpoints, errores, breaker state. Es también la fuente de `Email/changes` para JMAP.
- Config PG obligatoria (S3 H2-H4, no negociable): `btree_gin`, `plan_cache_mode=force_custom_plan` en conexiones de búsqueda, `STATISTICS 4000` en `tsv`, `fastupdate=on` (default). Pool separado con `statement_timeout` para rank/count (S3 H5).

**IDs estables (S2 H8 — sin OBJECTID):** `message_id` interno = surrogate; identidad de contenido = `sha256` del blob crudo (dedupe + sobrevive moves); identidad IMAP = `(mailbox_id, uidvalidity, uid)` en `message_state`. Un move = UPDATE de `message_state`, el contenido no se toca. Threading propio por `References`/`In-Reply-To` + fallback por subject normalizado (algoritmo JWZ simplificado; el detalle en la L2 de JMAP `Thread/*`).

**A6 — Arbitraje: modelo de labels híbrido con METADATA (resuelve el pendiente del ADR §9).**
- **Asignación** de label a mensaje = IMAP keyword (`$MoovL<n>` o keyword estándar si existe) — viaja por IMAP, visible para otros clientes, reconstruible.
- **Definición** de labels (nombre, color, orden, mapping keyword↔label) = **IMAP METADATA** (anotación privada del buzón raíz) — nuestro Dovecot lo anuncia (S2 caps) y go-imap lo soporta (`Enable(METADATA)` ya probado en S2). Así la definición TAMBIÉN es reconstruible desde Dovecot y la invariante "cache reconstruible" queda intacta.
- Límite práctico de keywords en Maildir: si una instalación lo alcanza, el engine rechaza crear el label con error claro (no hay labels "solo-DB" silenciosos).
- **Tarea de validación V1 (dentro de E2):** probar en nuestro Dovecot límites reales de METADATA (tamaño de anotación, persistencia tras `doveadm`) y cuántas keywords tolera Maildir en la práctica antes de degradar. Si METADATA falla la validación → fallback documentado: definición en DB + export/import explícito, marcado como no-reconstruible.

### 2.4 Pipeline de parseo (S4)

Cascada **go-message → enmime → raw blob** (bidireccional probada, S4 H1). El blob crudo SIEMPRE se persiste primero (content-addressed); el parseo es una derivación reintentable (`reparse` por versión de parser).

Mitigaciones obligatorias, cada una con su test del corpus (`testdata/mime-corpus/` corre en CI en cada PR):
- Caps propios: profundidad ≤100, partes ≤1000, tamaño total configurable; excederlos = `parse_status='failed'` (S4 H8).
- Nunca descartar bytes parciales en error de decode; marcar la parte como parcial (S4 H5).
- Post-proceso RFC 2047: `=?…?=` residual → retry con Raw encodings (S4 H4).
- Cascada de charset: declarado → `chardet` → windows-1252, con flag `charset_guessed` (S4 H6).
- Descenso recursivo en `message/rfc822` (con los caps globales) para indexar forwards (S4 H7).
- Both-fail → `parse_status='failed'` + salvage del body como texto plano único si es legible (S4 H3). La tasa de `failed` es métrica con alerta (regla R4).
- CR-only: pre-normalización de line-endings antes de la cascada, decisión explícita (S4 H9).

### 2.5 Algoritmo de sync (S2 + S3)

**Initial sync por cuenta (usable rápido, completo después — ADR §6 backfill):**
1. `LIST` + STATUS → árbol de mailboxes con roles.
2. INBOX últimos 30 días: FETCH headers+bodies → parse → insert normal (el índice ya existe; 0,25 ms/msg — S3 H8). La PWA es usable al terminar esta fase.
3. Backfill del histórico por checkpoints (mailbox, rangos UID descendentes), interrumpible y reanudable.
4. **Migración masiva de instalación** (caso Crash, 89 cuentas): camino separado con COPY + build de índices GIN al final (S3 H6: 2.063 filas/s CPU-bound en `to_tsvector` — presupuestar workers de parseo por core, no por cuenta).

**Incremental:** `SELECT (QRESYNC (uidvalidity modseq))` al reconectar → `VANISHED (EARLIER)` + FETCH de cambios (S2 H1). `UID FETCH (CHANGEDSINCE … VANISHED)` en conexión viva. UIDVALIDITY cambiado = invalidar mailbox y resync (el contenido se recupera por sha256 sin re-descargar blobs que ya tenemos).

**Watcher (1 conexión por cuenta activa — S2):** `NOTIFY SET STATUS (PERSONAL …)` con el encoder parcheado (S2 H4/H5: solo con `STATUS` los cambios de flags en carpetas no seleccionadas son visibles) + loop IDLE de mantenimiento (S2 H9). Evento → encolar FETCH batcheado por mailbox (S2 H7: notification-only). `NOTIFICATIONOVERFLOW` → resync completo de la cuenta. Cuentas sin sesión activa: watcher se apaga tras N min; reconciliación programada.

**Reconciler defensivo:** pasada periódica (configurable, default 6 h) STATUS de todos los mailboxes vs estado local — atrapa cualquier evento perdido (precedente de regresiones NOTIFY en Dovecot — research S2).

**Escrituras hacia IMAP (flags/moves fase 1b):** toda escritura condicional se verifica por read-back mientras `MODIFIED` no esté expuesto (S2 H6); batcheo de flag updates (S3 H9: 23x más barato). Outbox transaccional según ADR §4.

**Resiliencia:** pool acotado de conexiones por cuenta (watcher + N workers, default 2), backoff con jitter, circuit breaker por cuenta (anti fail2ban — ADR §4), reconexión distinguiendo `ECONNRESET` (el error wrapping post-beta.8 lo permite — S2).

### 2.6 Conexión y TLS

STARTTLS en `dovecot:143` dentro de la red Docker. Verificación de cert con `ServerName` configurable (el cert interno es del hostname público — S1 H2); **jamás** un switch global de ignorar certs. Capabilities se prueban **post-login** (S2: NOTIFY no aparece antes).

## 3. Stories con ACs (épicas de fase 1)

| # | Épica | ACs clave (test obligatorio por AC — política del grupo) |
|---|---|---|
| E1 | **Scaffolding + CI** | Módulo Go + estructura §2.1; CI: build/vet/lint/test + corpus MIME completo + gofmt; LICENSE AGPL-3.0, CONTRIBUTING, plantillas; `docker-compose.dev.yml` con PG17 configurado (las 3 configs S3 aplicadas por migración, verificadas por test) |
| E2 | **`internal/imap`** | Vendor + patch set aplicado con suite verde; interfaz cubre: connect/login STARTTLS, LIST/STATUS, SELECT QRESYNC, FETCH (headers/bodies/CHANGEDSINCE/VANISHED), IDLE, NOTIFY (encoder corregido), METADATA; **V1 (labels/METADATA) ejecutada y documentada**; ningún import de go-imap fuera del paquete (test de arquitectura con lint rule) |
| E3 | **`internal/store` + `internal/blob`** | Migraciones idempotentes; schema §2.3; blob store: write-once sha256, refcount, GC con test de concurrencia; benchmark de humo en CI (aviso si p95 de las queries S3 #1-#8 se degrada >2x contra corpus chico) |
| E4 | **`internal/parser`** | Cascada completa §2.4; 110/110 casos del corpus con el resultado esperado por manifest; fuzzing básico (go-fuzz sobre los generadores del corpus) sin panics; salida `ParsedMessage` estable (contract §4.2); hook de sanitización HTML declarado pero no implementado (no-scope) |
| E5 | **Initial sync** | Cuenta nueva usable (INBOX 30d) en <60 s con buzón de 10k; backfill reanudable tras kill -9 en cualquier punto (test de crash-recovery); camino de migración masiva con COPY medido contra corpus sintético |
| E6 | **Incremental + watcher** | Delta tras reconexión offline (flags+expunge+new) correcto contra Dovecot real (promover harness S2 a integración); evento NOTIFY → mensaje visible en store <1 s (test de integración); overflow → resync automático; reconciler encuentra y repara una divergencia inyectada |
| E7 | **Auth + aprovisionamiento** | Flujo ADR §4 completo contra API Mailcow real (staging); password de usuario nunca persiste (test que lo demuestra); app password cifrada AES-256-GCM con master key por env/file, rotación documentada |
| E8 | **Observabilidad** | Métricas Prometheus: lag de sync por cuenta, tasa `parse_status=failed` (con alerta — R4), profundidad de cola, breaker state; logs estructurados; `/healthz` |

Orden: E1 → E2/E3/E4 en paralelo (scopes disjuntos) → E5 → E6 → E7/E8. Ningún teammate toca archivos fuera de su épica (regla de Agent Teams).

## 4. Contracts (definidos ANTES de paralelizar — regla L2)

### 4.1 `internal/imap` (lo que el resto del engine puede pedir)

```go
type Client interface {
    Connect(ctx, Config) error                    // STARTTLS, login, ENABLE QRESYNC, caps post-login
    ListMailboxes(ctx) ([]MailboxInfo, error)     // con roles SPECIAL-USE y STATUS
    SelectQResync(ctx, mailbox, uidvalidity, modseq) (SelectResult, error) // incluye VanishedUIDs
    FetchChanges(ctx, since modseq) (iter, error) // CHANGEDSINCE + VANISHED
    FetchMessages(ctx, uids, FetchSpec) (iter, error) // headers | full; streaming
    Watch(ctx, WatchSpec) (<-chan Event, error)   // NOTIFY+IDLE; Event: MailboxChanged{name} | Overflow
    StoreFlags(ctx, uids, delta, unchangedSince) (StoreResult, error) // read-back interno hasta MODIFIED
    Metadata(ctx) MetadataOps                     // get/set anotaciones (labels A6)
}
```

### 4.2 `internal/parser`

```go
func Parse(raw io.Reader, limits Limits) ParsedMessage
type ParsedMessage struct {
    Status      ParseStatus   // ok | partial | failed
    Parser      string        // "go-message" | "enmime" | "salvage"
    Headers     CanonHeaders  // decodificados, con flags charset_guessed / rfc2047_retried
    Parts       []Part        // árbol aplanado con profundidad, incluye rfc822 descendido
    TextForFTS  string        // lo que va al tsv (por peso: subject/addrs/body)
    Defects     []Defect      // trazable al caso de corpus cuando aplique
}
```

### 4.3 Store / JMAP (el contract con la futura capa API)

- La capa JMAP **solo lee el store** (nunca IMAP) para `Email/get|query`, y **encola intents** (flag/move/send) que el sync engine ejecuta — el schema §2.3 + tabla `intents` son el contrato; `sync_log` alimenta `Email/changes`.
- Queries de búsqueda: siempre `account_id` + LIMIT; counts capeados; ranking acotado en pool aparte (decisiones de producto S3 H5 — la capa JMAP no puede emitir queries fuera de ese repertorio: se expone como métodos del store, no SQL).

## 5. Riesgos abiertos que esta fase debe vigilar

1. Bloat GIN bajo churn sostenido — soak test antes de producción (S3, mitigado por A5 que saca los flags del camino del tsv).
2. PgBouncer vs `force_custom_plan` — verificar antes de introducir pooler (S3 H3).
3. Mantenimiento del patch set en cada bump de go-imap (S2) — mitigado con upstream PRs propios.
4. METADATA en Dovecot con configs exóticas de instalaciones ajenas — V1 lo mide en la nuestra; la guía de instalación documentará el requisito.
5. Stemming/calidad de recall (S3 H10) — evaluar dual-config es/en durante E3, decisión antes del MVP.

## 6. Aprobación

- [x] Arbitrajes A5, A6 y A7: **delegados por Diego al director técnico** (2026-08-09, "te lo dejo a tu propia responsabilidad usando las mejores prácticas") y firmados por el director con la evidencia de los spikes S2/S3/S4. La validación V1 (E2) es la red de seguridad de A6.
- [x] Orden de épicas aprobado por la misma delegación; E1 arranca de inmediato.
