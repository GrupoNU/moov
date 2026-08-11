# Spec L2 — Servidor JMAP de Moov Mail (fase 1: read-only)

> **Estado:** ACEPTADA — arbitrajes firmados por el director técnico bajo la delegación vigente (regla 2; delegación de Diego 2026-08-09) · **Fecha:** 2026-08-11
> **Autor:** Fable 5 (director técnico) · **Nivel:** L2
> **Base:** ADR-001 §2 (JMAP estándar, subset por fases) · research 02 (JMAP deep dive) · L2-sync-engine §4.3 (el contrato store↔JMAP) · S1 H7 (CORS/rutas) · `jmap-perl` como plano de referencia del mapeo
> **Hito de cierre:** **Bulwark lee mail real a través de NUESTRO servidor** en el VPS (reemplazando al jmap-proxy de S1), pasando el JMAP TestSuite en CI.

---

## 1. Objetivo y no-scope

Servidor JMAP (RFC 8620 core + RFC 8621 mail) en Go, **read-only** (fase 1 del ADR): un cliente JMAP de terceros navega buzones, lee mails, busca y sigue cambios — contra el store que el sync engine mantiene. **No-scope fase 1:** escritura (`Email/set`, submission), push SSE (fase 2), SearchSnippet/Sieve/Quota (fase 3), PWA propia.

## 2. Decisiones de diseño

### 2.1 Topología

```
internal/jmap/            — protocolo: tipos RFC 8620/8621, parseo de Request, method dispatch,
                            back-references (#ids/resultOf), Invocation batching, errores tipados
internal/jmap/mail/       — los métodos de mail: Mailbox/*, Email/*, Thread/*, mapeo store→JMAP
internal/jmaphttp/        — capa HTTP: session endpoint, rutas, auth, CORS, límites, blob download
cmd/moovd                 — monta el server JMAP (mismo daemon; puerto/config propios)
```

stdlib `net/http` (Go 1.22+ mux con patrones) — sin framework: la superficie es chica y el control de límites/streaming importa más que la ergonomía de un router.

### 2.2 Arbitraje J-A1 — Auth de fase 1: Basic contra credencial aprovisionada

Bulwark habla HTTP Basic (así funcionó en S1). Fase 1: `Authorization: Basic user:password` donde la password se valida **contra Dovecot vía IMAP LOGIN** (la fuente de verdad de auth, ADR §4), con cache en memoria de resultado positivo (hash argon2id de la password, TTL 10 min, invalidable) para no pagar un LOGIN por request. Solo cuentas ya aprovisionadas (`moovctl account add`) sirven datos — un LOGIN válido de una cuenta no aprovisionada devuelve 403 con mensaje claro, jamás aprovisiona solo. El endpoint REST de login/aprovisionamiento web y los Bearer tokens son fase 2 (los necesita la PWA, no Bulwark). El rate-limit de intentos fallidos protege a Dovecot del fail2ban propio (breaker por IP+cuenta).

### 2.3 Superficie fase 1 (RFC 8620/8621)

| Pieza | Contenido |
|---|---|
| Session | `/.well-known/jmap` → objeto session (capabilities `core` + `mail`), `apiUrl`, `downloadUrl`, `uploadUrl` (501 en fase 1), `eventSourceUrl` (501), accountId estable |
| Core | `Core/echo`, Request/Response con `methodCalls`, back-references completas, `maxCallsInRequest`/`maxObjectsInGet` y demás límites declarados y APLICADOS |
| Mailbox | `get`, `changes` (desde `sync_log`), roles SPECIAL-USE mapeados como S1 validó, contadores `totalEmails`/`unreadEmails`/`totalThreads`/`unreadThreads` |
| Email | `get` (properties selectivas: metadata desde `messages`, bodyValues desde blob+parser bajo demanda, `fetchTextBodyValues`/`fetchHTMLBodyValues`, `maxBodyValueBytes`), `query` (filtros: mailbox, texto FTS, from/to/subject, fechas, flags; sort: date, y relevancia SOLO como opt-in acotado — decisiones S3 H5), `changes`, `queryChanges` → `cannotCalculateChanges` (legítimo, ADR §2) |
| Thread | `get` — threading propio del store (JWZ simplificado, L2-sync-engine §2.3) |
| Blob | download `{blobId}` con auth + verificación de pertenencia a la cuenta; `Content-Type` seguro (nunca ejecutable inline: `application/octet-stream` + `Content-Disposition` salvo tipos allowlisted) |
| Keywords | mapeo flags/keywords IMAP ↔ JMAP keywords (`$seen`, `$flagged`, `$answered`, `$draft` + custom) desde `message_state` |

Regla dura heredada de §4.3: **la capa JMAP solo lee el store** — nunca IMAP — y solo a través del repertorio de métodos tipados (counts capeados "199+", relevancia acotada en pool aparte). `Email/query` con `position`/`anchor`/`limit` se implementa sobre ese repertorio; `total` responde el count capeado con `total` omitido cuando excede (el RFC lo permite si `calculateTotal:false`).

### 2.4 CORS y rutas (S1 H7 — requisito real de clientes web)

CORS configurable por lista de origins desde el día 1: preflight OPTIONS correcto en TODAS las rutas (session, api, download), `Access-Control-Allow-Headers: Authorization, Content-Type`. Todas las rutas de sesión documentadas en un solo lugar (`internal/jmaphttp/routes.go`).

### 2.5 Conformidad verificable (regla del ADR: TestSuite en CI desde el día 1)

- **JMAP TestSuite** (jmapio) corriendo contra el server en CI con una cuenta semilla (PG service container + fixtures del store; sin Dovecot — el server no lo necesita). Los tests que exigen escritura se marcan skip explícito con razón (fase 2), nunca silencioso.
- `jmap-perl` sigue corriendo en el VPS como **oráculo**: ante duda de mapeo, se compara respuesta contra él (así se decidió en S1 H6).
- Golden tests propios de Request/Response para los caminos que el TestSuite no cubre (límites, back-references anidadas, errores tipados).

### 2.6 Seguridad fase 1

Auth §2.2 · blob download con chequeo de cuenta y headers seguros (§2.3) · límites de tamaño de request/respuesta aplicados · sin HTML sanitizado todavía: fase 1 sirve `bodyValues` texto y HTML CRUDO **solo vía API autenticada** — la sanitización de 3 capas (ADR §5) es requisito de la PWA (fase 2) y Bulwark hace la suya client-side; documentado explícitamente en SECURITY.md al cerrar J4 · rate limiting por cuenta.

## 3. Épicas

| # | Épica | ACs clave |
|---|---|---|
| J1 | **Core JMAP + HTTP + auth** | Session object conforme; dispatch con batching y back-references (goldens); auth Basic→IMAP LOGIN con cache y rate-limit (tests con fake + integración real); CORS completo (test preflight en cada ruta); límites declarados=aplicados |
| J2 | **Mailbox/Thread/Email-get + blobs** | `Mailbox/get` con roles y contadores correctos vs fixtures; `Email/get` con properties selectivas y bodyValues bajo demanda (blob+parser, cache de parse si hace falta); `Thread/get`; blob download con pertenencia + headers seguros; paridad verificada contra jmap-perl en 5 mailboxes de referencia |
| J3 | **Email/query + changes** | `query` sobre el repertorio del store (todos los filtros/sorts de §2.3, anchor/position correctos vs fixtures); `Email/changes` + `Mailbox/changes` desde `sync_log` con la semántica de tombstones de E3; `queryChanges` → `cannotCalculateChanges`; property-based tests de paginación |
| J4 | **Conformidad + deploy + hito Bulwark** | JMAP TestSuite en CI (verde en lo aplicable, skips justificados); compose de deploy en el VPS (moovd+PG en `mailcowdockerized_mailcow-network`, VPN-only); Caddy de S1 rutea a NUESTRO server; **Bulwark en navegador leyendo `moov-test` end-to-end**; E8-lite: métricas HTTP/JMAP básicas + logs estructurados en el server |

Orden: J1 → J2 ∥ J3 (scopes disjuntos: get-family vs query/changes) → J4. E8 completa (métricas del engine) se pliega en J4.

## 4. Contracts

- `internal/jmap` no importa store: define interfaces (`MailboxReader`, `EmailReader`, `ChangesReader`, `BlobReader`) que `internal/jmap/mail` implementa sobre `internal/store`+`internal/blob`+`internal/parser` — mismo patrón de encapsulamiento que `internal/imap`.
- El accountId JMAP = id de `accounts` codificado (opaco, estable). blobId = sha256 hex del blob (ya content-addressed, gratis).
- Errores: mapa único RFC 8620 §3.6 (`unknownMethod`, `invalidArguments`, `serverPartialFail`, …) en `internal/jmap/errors.go` — nunca strings ad hoc.

## 5. Riesgos

1. El TestSuite puede exigir semánticas que el subset read-only no da — cada skip se documenta con el issue de fase 2 que lo cubrirá.
2. `Email/get` con bodyValues re-parsea bajo demanda — si el costo aparece en Bulwark real, cache de derivados por (blobId, parser-version) como extensión del store (decisión en J2 con números).
3. Paridad jmap-perl: divergencias menores se documentan como decisiones de mapeo (ADR §2 ya registró tres).

## 6. Aprobación

- [x] Arbitraje J-A1 (auth fase 1) y superficie §2.3 firmados por el director bajo la delegación vigente. Diego puede vetar antes de J4 (deploy) sin costo estructural.
