# 00 — Síntesis auditada de la Fase 0

> **Proyecto:** Webmail open source Gmail-class para Mailcow — primer producto open source de NU Desarrollos Conscientes
> **Fecha:** 2026-08-07
> **Auditor:** Director del proyecto (Fable 5), sobre 4 informes de research (agentes Opus 5)
> **Regla de arbitraje (Diego):** siempre lo más potente del mercado actual, con los estándares más altos y las mejores prácticas
> **Vara de calidad (Diego):** Gmail-class en diseño Y performance — benchmark contra Gmail/Fastmail/Superhuman, no contra otros webmails OSS

---

## 1. Veredicto de la auditoría

Los 4 informes ([01](01-competitive-landscape.md), [02](02-jmap-deep-dive.md), [03](03-mailcow-integration.md), [04](04-sync-engine-prior-art.md)) son de calidad alta, con fuentes verificables y hallazgos convergentes. La tesis del proyecto queda **validada por cuádruple vía independiente**:

1. **El hueco existe y está vacío** (01): ningún proyecto vivo ocupa la fila "sync engine + índice propio + API moderna + PWA contra Dovecot/Mailcow".
2. **No hay atajo** (02): Dovecot no tiene ni tendrá JMAP; el sync engine intermedio es la única arquitectura posible.
3. **La integración es limpia** (03): Mailcow expone todas las superficies necesarias (IMAP/SMTP/Sieve/API/DAV) sin modificar nada de Mailcow.
4. **El prior art enseña cómo no morir** (04): Nylas, Mailspring, Nextcloud Mail y Delta Chat dejaron documentados los errores exactos a evitar.

**Verificación empírica en nuestro VPS (2026-08-07, solo lectura):** `SKIP_FTS=n` → Flatcurve activo. `COMPOSE_PROJECT_NAME=mailcowdockerized` → red `mailcowdockerized_mailcow-network`. Sin `DOVECOT_MASTER_USER` estático. Variables `SOLR_*` huérfanas en `mailcow.conf` (resto de la migración Solr→Flatcurve de ene-2025; corregir doc del repo que aún dice "Solr habilitado").

## 2. Conflictos detectados entre agentes y arbitraje

### Arbitraje A1 — Capa de API: JMAP estándar vs REST propia ⚖️ **RESUELTO: JMAP estándar**

- El informe 02 recomienda **JMAP estándar por fases** (RFC 8620/8621).
- El informe 04 recomienda **REST propia "inspirada en JMAP"**, con el argumento de que no existe librería servidor JMAP usable en Go.

**Resolución del director (regla "lo más potente + estándares más altos"): JMAP estándar, subset por fases.**
El argumento del informe 04 es real pero no decisivo: la ausencia de librería significa que escribimos la capa JMAP nosotros (~20-25% del esfuerzo según 02), no que la API deba ser propietaria. A cambio: spec ya diseñada por la industria (Fastmail/IETF), **JMAP TestSuite como oráculo de conformidad en CI desde el día 1**, clientes de terceros como fallback y testing (Bulwark/TMail corren hoy sobre jmap-perl — un proxy IMAP), apps móviles (Twake Mail), librerías TS mantenidas para nuestro propio frontend (jmap-jam/jmap-kit), y portabilidad a cualquier backend JMAP. `jmap-perl` (MIT, vivo, 132/132 tests) es el plano de ingeniería. Una API propia es el techo bajo que la regla de arbitraje prohíbe; se acepta solo como secuencia interna de implementación, nunca como superficie pública.

### Arbitraje A2 — Búsqueda: índice propio completo vs híbrido con Flatcurve ⚖️ **RESUELTO: índice propio completo**

- El informe 03 propone híbrido: metadatos propios + cuerpo delegado a Flatcurve vía `IMAP SEARCH`.
- El informe 04 propone indexar **siempre** el texto extraído de todos los mensajes (1-3% del tamaño; ~1-3 GB por cada 100 GB de buzón).

**Resolución del director (vara Gmail-class): índice propio completo.**
La búsqueda instantánea <100 ms as-you-type con ranking por relevancia es EL diferenciador #1 del producto (01 §4.1) y `IMAP SEARCH` sobre Flatcurve no puede darla (round-trip + sin ranking + sin as-you-type). El costo de storage es marginal. Flatcurve queda como fallback opcional para cuerpos históricos fuera de ventana si algún caso extremo lo justifica. Motor: `tsvector`+`pg_trgm` en MVP con interfaz `Indexer` + comando `reindex` obligatorios desde el día 1, y **Meilisearch como fase 2 pre-evaluada** (el benchmark de 5M mensajes decide si entra directo al MVP — R8 del informe 04).

### Arbitraje A3 — ¿Reevaluar migrar a Stalwart? ⚖️ **RESUELTO: NO (decisión de Diego, ratificada)**

El informe 04 sugiere comparar formalmente contra "migrar a Stalwart y hacer solo la PWA". Descartado y documentado en ADR-001: (a) Mailcow es nuestra infraestructura productiva con reputación construida; (b) la niche desatendida es exactamente el parque instalado de Mailcow/Dovecot — migrar de servidor destruye la razón de ser del producto; (c) al exponer JMAP estándar (A1), nuestro webmail *además* funcionará contra Stalwart — obtenemos la opcionalidad sin pagar la migración.

### Convergencias sin conflicto (adoptadas)

| Decisión | Fuente | Nota |
|---|---|---|
| Backend **Go 1.23+**, `go-imap/v2` vendorizado y encapsulado tras interfaz propia | 04 | Única lib con QRESYNC+CONDSTORE+IDLE+NOTIFY. Riesgo beta mitigado con vendoring |
| MIME: `go-message` primario + `enmime` fallback; corpus de mails patológicos en tests | 04 | |
| **PostgreSQL 17** metadata + blobs `.eml` filesystem content-addressed (sha256, dedupe, refcount) | 04 | "Somos cache, no fuente de verdad" — todo re-fetcheable de Dovecot |
| Cache de bodies **híbrido por recencia** (~6 meses), headers+texto FTS siempre | 04 | Lección Mailspring |
| Auth: `IMAP LOGIN` de validación → **app password** aprovisionada vía API, cifrada, password del usuario descartada | 03 | Revocable desde la UI de Mailcow. Sin master user en el request path |
| Envío por **`postfix:587`** (hereda DKIM/rate limits) + outbox transaccional + APPEND a `\Sent` | 03 | No atómico → outbox con reintentos |
| Push: IDLE selectivo (INBOX + sesiones activas) + validar **NOTIFY** en spike; **SSE** al browser | 03+04 | SSE es además el push nativo de JMAP (EventSource) — coherente con A1 |
| Deploy: **stack propio** con `mailcowdockerized_mailcow-network` como red externa, subdominio propio con TLS propio | 03 | Patrón B. `./update.sh` de Mailcow no nos ve |
| **Regla dura: NUNCA tocar el filesystem de vmail** (`maildir_very_dirty_syncs=y`) — todo por IMAP | 03 | Violación = corrupción de buzones |
| Seguridad: sanitización 3 capas (bluemonday + DOMPurify + `iframe sandbox` sin `allow-scripts` + CSP), proxy de imágenes con HMAC + anti-SSRF | 04 | Los 2 riesgos críticos del proyecto |
| **No apagar SOGo**: CalDAV/CardDAV/ActiveSync se quedan; reuso post-MVP vía `/SOGo/dav/` con app password `dav_access` | 01+03 | JMAP Calendars sigue en draft — no hay estándar estable para reemplazarlo |
| Threading JWZ incremental + normalización de prefijos localizados (`RV:`, `AW:`…) + fallback subject con ventana temporal | 02+04 | |
| Un Email = una Mailbox (semántica IMAP), como jmap-perl y Cyrus | 02 | Evita el problema más espinoso del mapeo |
| Frontend: React/TypeScript **PWA**, optimistic UI, virtualización, shortcuts Gmail, command palette | 01 | Features P0/P1/P2 en 01 §7 |

## 3. Riesgos consolidados (top del proyecto)

1. **XSS por bypass de sanitizer** (crítico) — mitigación 3 capas; la que salva es `iframe sandbox`.
2. **SSRF del proxy de imágenes** contra la red interna del VPS (crítico) — HMAC + rechazo de IPs privadas post-DNS y post-redirect.
3. **Escritura bidireccional IMAP** (cola de operaciones, conflictos con otros clientes) — el mayor riesgo de ingeniería puro; patrón local/remote de Mailspring + "el servidor IMAP gana siempre".
4. **`go-imap/v2` en beta ~18 meses** — vendoring + encapsulación + presupuesto de fork.
5. **tsvector al borde con ~5M mensajes** — benchmark bloqueante; Meilisearch pre-evaluado.
6. **Sostenibilidad humana** (el patrón de muerte #1 del rubro) — ventaja estructural: resolvemos dolor propio; nunca big-bang, valor navegable desde la semana 1.
7. **Límite de 26 keywords/carpeta en Maildir** — condiciona el modelo de labels; decisión abierta D3 antes del schema.
8. **Backfill inicial** (89 buzones Crash, 584 GB como caso extremo) — sync por fases con checkpoint; PWA usable tras fase 2 (headers 30 días INBOX).

## 4. Spikes de validación (bloqueantes antes de codear en serio)

| # | Spike | Valida | Duración est. |
|---|---|---|---|
| S1 | `jmap-perl` en Docker contra cuenta de prueba de nuestro Mailcow + conectar Twake Mail/Bulwark | JMAP-sobre-Dovecot funciona en nuestro entorno; UX de referencia real | ~1-2 días |
| S2 | `go-imap/v2` beta.8 contra nuestro Dovecot: QRESYNC (`VANISHED EARLIER`), CONDSTORE (`CHANGEDSINCE`), **NOTIFY multi-mailbox** | La base técnica del sync engine; si NOTIFY funciona, colapsa el fan-out de conexiones | ~2 días |
| S3 | Benchmark `tsvector`+GIN con corpus sintético de 5M mensajes en hardware del VPS | Si Meilisearch entra al MVP o a fase 2 | ~1 día |
| S4 | Corpus de MIME patológico (suite de tests) | Robustez del parser antes de escribirlo | continuo |

## 5. Decisiones abiertas para Diego

| # | Decisión | Recomendación del director |
|---|---|---|
| D-1 | **Nombre del producto** | A definir (branding NU Desarrollos Conscientes) |
| D-2 | **Licencia** | **AGPL-3.0** (protege contra SaaS cerrado; la usan Bulwark, SnappyMail, Nextcloud Mail, Stalwart) |
| D-3 | **Repo**: nuevo repo público separado de VPS_Mail | Sí — es producto, no infra. Migrar `docs/webmail-project/` allí al crearlo |
| D-4 | Modelo de labels ante el límite de 26 keywords (D3 del informe 03) | Híbrido (c): keywords para las más usadas + resto en nuestra DB — resolver en spec L2 |
| D-5 | Confirmar deploy Patrón B (segundo stack a operar en el VPS) | Sí (ya recomendado) |

## 6. Correcciones a documentación del repo

- `CLAUDE.md` (VPS_Mail): dice "Solr habilitado (búsqueda full-text para 300 GB Crash)" → **desactualizado**. Real: Flatcurve/Xapian (`SKIP_FTS=n`, `FTS_HEAP=128`, `FTS_PROCS=1`). Solr fue eliminado de Mailcow en ene-2025.
- `mailcow.conf` en el VPS conserva `SKIP_SOLR`/`SOLR_HEAP` huérfanas (inofensivas; limpiar en próximo mantenimiento).
