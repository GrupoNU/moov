# ADR-001 — Arquitectura de Moov Mail (webmail open source para Mailcow)

> **Estado:** ACEPTADO — aprobado por Diego el 2026-08-07
> **Fecha:** 2026-08-07
> **Decisores:** Diego (Director), Fable 5 (director técnico del proyecto)
> **Basado en:** Fase 0 de research — [síntesis auditada](../research/00-sintesis-fase0.md) y los 4 informes de `docs/research/`
> **Reglas rectoras:** (1) vara Gmail-class en diseño y performance; (2) ante opciones en conflicto, siempre lo más potente del mercado con los estándares más altos y las mejores prácticas

---

## Contexto

SOGo, el webmail de Mailcow, es arquitectónicamente incapaz de dar experiencia Gmail-class (lentitud documentada con ~3.000 mensajes — mailcow#4011, etiquetado `upstream`). Ningún webmail moderno funciona contra Dovecot: los modernos (Bulwark, Twake) exigen servidores JMAP nativos (Stalwart/James), y Dovecot declaró que no implementará JMAP. El parque instalado de Mailcow/Dovecot no tiene opción de clase mundial. Este proyecto —primer producto open source de NU Desarrollos Conscientes— la construye.

## Decisión

### 1. Arquitectura: Opción B — sync engine con estado propio

Un backend que sincroniza los buzones Dovecot (IMAP) a una base propia con índice full-text, y sirve una API moderna a una PWA. Se instala como stack Docker independiente **al lado** de Mailcow, sin modificarlo. Somos **cache, no fuente de verdad**: Dovecot gana siempre; cualquier dato local es reconstruible.

**Descartado — IMAP-directo** (Roundcube/SnappyMail-style): techo arquitectónico; imposible instant search, push, offline, undo send.
**Descartado — migrar a Stalwart**: destruye la razón de ser (servir al parque instalado de Mailcow) y nuestra operación productiva. Al exponer JMAP estándar, el webmail igualmente funcionará contra Stalwart — opcionalidad sin migración.

### 2. API pública: JMAP estándar (RFC 8620/8621), subset por fases

- Fase 1: read-only (Session, `Mailbox/*`, `Email/get|query|changes`, `Thread/*`, blobs, batching + back-references).
- Fase 2: escritura (`Email/set`, `Mailbox/set`), envío (`EmailSubmission/set` + `onSuccessUpdateEmail`), push SSE (EventSource).
- Fase 3: SearchSnippet, SieveScript (RFC 9661 → ManageSieve), Quota (RFC 9425), VacationResponse, PushSubscription/VAPID, WebSocket (RFC 8887).
- **JMAP TestSuite en CI desde el día 1.** Plano de referencia: `jmap-perl` (MIT, 132/132 tests).
- Decisiones de mapeo registradas: un Email = una Mailbox (semántica IMAP); `cannotCalculateChanges` es respuesta legítima para `queryChanges`; fusión de threads emite destroyed+created (el frontend lo tolera).
- Endpoints auxiliares fuera del scope JMAP (login/aprovisionamiento/admin) son REST mínimos — la spec no cubre auth de sesión.

**Descartado — API REST propia** (recomendación del informe 04): viola la regla de estándares más altos; pierde test suite externo, clientes de terceros, librerías cliente y portabilidad. La ausencia de librería servidor JMAP en Go implica escribir la capa nosotros (~20-25% del esfuerzo), no bajar el estándar.

### 3. Stack

| Capa | Elección |
|---|---|
| Backend | **Go 1.23+** |
| Cliente IMAP | `emersion/go-imap/v2` — **vendorizado, pinneado y encapsulado** tras interfaz propia (`internal/imap`) |
| MIME | `go-message` (primario, streaming) + `enmime` (fallback); un mensaje que no parsea jamás frena el sync |
| Metadata | **PostgreSQL 17** (JSONB + columnas generadas; transaccionalidad para la cola de tareas) |
| Blobs | Filesystem content-addressed sha256, dedupe global, refcount, GC |
| Cache de bodies | Híbrido por recencia (~6 meses); headers + texto FTS de TODO el histórico, siempre |
| Búsqueda | **Índice propio completo**: `tsvector`+GIN+`pg_trgm` (MVP) tras interfaz `Indexer` + comando `reindex`; **Meilisearch** fase 2 (o MVP si el benchmark S3 lo exige) |
| Push server→browser | **SSE** (nativo de JMAP; reconexión + `Last-Event-ID`) |
| Frontend | **React + TypeScript, PWA** (cliente JMAP: jmap-jam o propio) |

### 4. Integración con Mailcow (sin tocar Mailcow)

- **Auth:** login del usuario → validación por `IMAP LOGIN` real contra `dovecot:143` (STARTTLS) → aprovisionamiento vía API de app password `webmail-*` con scope `imap+smtp+sieve` → se cifra (AES-256-GCM, master key fuera de la DB) y **se descarta la password del usuario**. Sin app password no hay cuenta (no degradamos a guardar passwords). Sin master user en el request path. Arquitectura agnóstica al IdP (OIDC de Mailcow no cubre IMAP).
- **Envío:** `postfix:587` (submission) con la app password → hereda DKIM, rate limits y logging de Mailcow. Outbox transaccional: SMTP → APPEND a `\Sent`; nunca reintentar SMTP tras 250; dedupe por Message-ID.
- **Sync:** CONDSTORE/QRESYNC como fuente de deltas; IDLE solo en INBOX de sesiones activas; NOTIFY si el spike S2 lo valida; pool de conexiones acotado (lección Nextcloud Mail); backoff con jitter + circuit breaker por cuenta (anti fail2ban).
- **Marcar spam** = `MOVE` a la carpeta `\Junk` (imapsieve reporta a Rspamd gratis).
- **Deploy:** stack propio (`/opt/webmail/`) unido a `mailcowdockerized_mailcow-network` como red externa + red interna propia para Postgres; subdominio con TLS propio. `./update.sh` de Mailcow no nos ve.
- **Regla dura:** NUNCA tocar el filesystem de vmail (`maildir_very_dirty_syncs=y`). Todo por IMAP. Ningún volumen de vmail montado en nuestros contenedores.
- **SOGo no se apaga:** CalDAV/CardDAV/ActiveSync quedan en SOGo (JMAP Calendars sigue en draft). Post-MVP: lectura de agenda vía `/SOGo/dav/` con `dav_access`.

### 5. Seguridad (estándares más altos desde el día 1)

- HTML de mails: **3 capas** — bluemonday server-side (allowlist estricta) + DOMPurify client-side + render en `<iframe sandbox>` sin `allow-scripts`/`allow-same-origin` con CSP `default-src 'none'`. CSS no inlineado se strippea (anti "Spy Sheets").
- Imágenes remotas: bloqueo por defecto + proxy propio con HMAC obligatorio, rechazo de IPs privadas/loopback tras resolver DNS y tras cada redirect, límites de tamaño/timeout, cache.
- Credenciales cifradas at-rest desde el commit 1 (la falla explícita de Nylas).
- Corpus de MIME patológico en la suite de tests antes de escribir el parser.

### 6. Criterios de aceptación Gmail-class (medibles, no aspiracionales)

- Búsqueda instantánea as-you-type < 100 ms percibidos, con ranking.
- Toda acción de usuario (archivar, marcar, mover) < 100 ms percibidos (optimistic UI + rollback).
- Inbox renderiza desde el índice local sin esperar IMAP; scroll fluido con 100k+ mensajes (virtualización + prefetch).
- Push real: mail nuevo aparece sin interacción (SSE), cero polling del cliente.
- Navegación completa por teclado con el vocabulario Gmail (`j/k`, `e`, `r`, `#`, `/`, `c`) — copiado, no inventado.
- Undo send (cola diferida 5-30 s).
- PWA instalable, responsive, dark mode de primera clase.
- Backfill: la PWA es usable con headers de 30 días de INBOX aunque el histórico siga sincronizando (checkpoint por fases).

### 7. Consecuencias

**Positivas:** único webmail moderno del parque Dovecot/Mailcow; conformidad JMAP verificable por CI; clientes de terceros (Twake móvil, Bulwark web) como upside y oráculo; toda pieza (engine/API/PWA) separable y reutilizable.
**Negativas asumidas:** implementamos servidor JMAP en Go desde cero (no existe librería); dependencia de `go-imap/v2` en beta (mitigada con vendoring y presupuesto de fork); operamos un segundo stack en el VPS; storage adicional (~1-3% del mail + bodies recientes + blobs deduplicados).

### 8. Validación previa al desarrollo (bloqueante)

S1 `jmap-perl` contra nuestro Mailcow + clientes reales · S2 QRESYNC/CONDSTORE/NOTIFY con go-imap beta.8 contra nuestro Dovecot · S3 benchmark tsvector 5M mensajes · S4 corpus MIME. Detalle en la [síntesis §4](../research/00-sintesis-fase0.md).

### 9. Decisiones de producto (resueltas por Diego, 2026-08-07)

- **Nombre:** Moov Mail
- **Licencia:** AGPL-3.0
- **Repo:** `github.com/GrupoNU/moov` (local `D:\git\moov`), separado de VPS_Mail
- Pendiente para la spec L2: modelo de labels ante el límite de 26 keywords Maildir (recomendación: híbrido).
