# Moov Mail

> Webmail open source Gmail-class para Mailcow/Dovecot — primer producto open source de NU Desarrollos Conscientes
> Repo público: `github.com/GrupoNU/moov` · Licencia: AGPL-3.0

## Estado

**2026-08-08:** Proyecto iniciado 2026-08-07. Fase 0 (research) completa, ADR-001 aceptado. **Spikes S1 y S2 VALIDADOS.** S1: JMAP-sobre-Dovecot funciona contra nuestro Mailcow real, incluyendo Bulwark (cliente de terceros) en navegador — `docs/spikes/S1-jmap-sobre-dovecot.md` (H1-H7). S2: base técnica del sync engine validada — QRESYNC/CONDSTORE/IDLE/NOTIFY correctos en nuestro Dovecot 2.3.21.1 (NOTIFY colapsa el fan-out: 1 conexión observa N carpetas); go-imap/v2 requiere **rama v2 pinneada + patch set propio** (PR #757 QRESYNC validado end-to-end, fix encoder NOTIFY, exponer MODIFIED) — `docs/spikes/S2-go-imap-dovecot.md` (H1-H9 + arbitraje A4). Stack de referencia S1 corriendo en el VPS mail (VPN-only): Caddy (:8090) → Bulwark + jmap-proxy → Dovecot, con buzón de prueba `moov-test@atmosfera.cloud` (credenciales en `credentials/` de VPS_Mail, git-ignored). **S4 VALIDADO (2026-08-09):** corpus de MIME patológico en `testdata/mime-corpus/` (110 casos, manifest-como-spec) + harness dual-parser — cascada go-message → enmime → raw blob confirmada bidireccional, 0 panics/hangs — `docs/spikes/S4-corpus-mime.md` (H1-H9). **S3 VALIDADO (2026-08-09):** tsvector+GIN alcanza la vara Gmail-class a 5M mensajes (8/10 shapes, margen 4x-30x, 607 qps concurrente) con 3 configs obligatorias (`gin(account_id,tsv)` compuesto, `force_custom_plan`, `STATISTICS 4000`); ranking/count exacto se resuelven por producto (count capeado, relevancia opt-in acotada, pool separado); **Meilisearch fuera del MVP** — `docs/spikes/S3-benchmark-fts.md` (H1-H11). **FASE DE SPIKES COMPLETA (4/4).** Spec L2 del sync engine redactada (`docs/specs/L2-sync-engine.md`, 2026-08-09): 8 épicas E1-E8, contracts, y arbitrajes A5 (estado volátil en tabla lateral), A6 (labels híbrido keywords+METADATA) y A7 (initial sync doble camino) — **ACEPTADA** (arbitrajes delegados por Diego al director técnico, 2026-08-09). **E1-E4 COMPLETAS** (2026-08-10): E1 scaffolding+CI; E2 `internal/imap` (vendor go-imap rama v2 + patch set 0001-0003 validado contra Dovecot real; V1 ejecutada: METADATA ok, **techo durable de keywords Maildir = 26** — corrección al A6); E3 `internal/store`+`internal/blob` (schema A5 con `message_state`, repertorio de búsqueda S3 como métodos, fix de unaccent en query-side); E4 `internal/parser` (cascada con 7 mitigaciones, corpus 110/110, 3 bugs cazados por fuzzing incl. DoS superlineal en enmime). **E5+E6+E7 COMPLETAS (2026-08-11): el sync engine funciona de punta a punta** — initial sync (10k en ~50 s, crash-recovery probado), incremental QRESYNC + watcher NOTIFY (mail nuevo → store en ~500 ms mediana contra Dovecot real), reconciler, y aprovisionamiento con crypto (ciclo real validado contra Mailcow). Falta E8 (observabilidad, chica). Siguiente bloque: **L2 del servidor JMAP ACEPTADA** (`docs/specs/L2-jmap-server.md`, 2026-08-11): 4 épicas J1-J4, hito de cierre = Bulwark leyendo mail vía nuestro server. J1 (core+HTTP+auth) en curso.

## Reglas rectoras del proyecto (fijadas por Diego — no negociables)

1. **Vara Gmail-class en diseño Y performance.** El benchmark es Gmail/Fastmail/Superhuman, nunca otros webmails open source. Los criterios son medibles (ADR-001 §6): búsqueda y acciones <100 ms percibidos, push real, teclado completo, undo send, PWA offline.
2. **Arbitrajes: siempre lo más potente del mercado actual, con los estándares más altos y las mejores prácticas.** La opción conservadora solo se acepta como fase intermedia explícita, nunca como destino.
3. **Es la cara open source de NU** — calidad de código, docs, tests y gobernanza son parte del producto.

## Jerarquía de trabajo (modelo de sesión)

- **Director/auditor: el modelo principal de la sesión** (preferencia de Diego: Fable 5, `/model claude-fable-5[1m]`). Dirige, arbitra entre agentes, audita todo lo que producen, sintetiza y firma decisiones. No delega la auditoría.
- **Subagentes: modelos más económicos según complejidad de la tarea** — `opus` (Opus 5) por defecto para research/implementación estándar; `sonnet`/`haiku` solo para tareas triviales/mecánicas. Nunca comprometer calidad en tareas críticas por ahorrar.
- Ante recomendaciones en conflicto entre agentes, el director arbitra con la regla 2 (lo más potente del mercado, estándares más altos) y documenta el arbitraje (como los A1-A3 de `docs/research/00-sintesis-fase0.md`).
- Trabajo paralelo: lanzar agentes independientes en simultáneo (como los 4 de la Fase 0); el director consolida al final.

## Arquitectura (resumen — el detalle manda en `docs/adr/ADR-001-arquitectura.md`)

- **Opción B:** sync engine (Go) que sincroniza Dovecot vía IMAP (CONDSTORE/QRESYNC/IDLE) a store propio (PostgreSQL 17 + blobs content-addressed sha256 + índice FTS) y expone **JMAP estándar** (RFC 8620/8621, subset por fases) a una **PWA React/TypeScript**. Push por SSE.
- **Mailcow no se toca jamás.** Stack Docker propio unido a `mailcowdockerized_mailcow-network`. Todo por IMAP/SMTP/Sieve/API. **NUNCA montar ni tocar el filesystem de vmail** (corrupción garantizada con `maildir_very_dirty_syncs`).
- **Dovecot es la fuente de verdad**; Moov es cache reconstruible.
- Auth: IMAP LOGIN de validación → app password vía API Mailcow (scope imap+smtp+sieve), cifrada AES-256-GCM, password del usuario descartada.
- Seguridad HTML: 3 capas (bluemonday + DOMPurify + iframe sandbox sin allow-scripts + CSP). Proxy de imágenes con HMAC + anti-SSRF.
- `go-imap/v2` SIEMPRE vendorizado y encapsulado tras `internal/imap` — nunca usar `imapclient` fuera de ese paquete.
- JMAP TestSuite en CI desde el día 1. `jmap-perl` (github.com/jmapio/jmap-perl) es el plano de referencia del mapeo IMAP↔JMAP.

## Convenciones

- Código, comentarios, commits e issues públicos: **inglés**. Comunicación con Diego: español.
- Commits: `tipo(scope): descripción` (feat/fix/docs/refactor/chore/test).
- Testing obligatorio (política de ingeniería del grupo): bug fix = test primero; feature = tests por AC.
- NUNCA push sin confirmación de Diego.
- El corpus de MIME patológico (`docs/research/04` §4.2) debe existir ANTES que el parser.

## Entorno de validación

- Mailcow real de referencia: VPS mail de Grupo NU (repo `D:\git\VPS_Mail`, IP-A 217.216.83.79, Tailscale `100.123.119.124`). `SKIP_FTS=n` (Flatcurve activo). Red: `mailcowdockerized_mailcow-network`.
- Cuentas de prueba para spikes: crear buzones dedicados (NUNCA usar buzones productivos de clientes para pruebas de escritura).
- Caso de estrés de referencia: buzones Crash (89 cuentas, 584 GB backup-only en Mailcow).

## Spikes pendientes (bloqueantes antes de codear el producto)

| # | Spike | Valida |
|---|---|---|
| S1 ✅ | jmap-perl + Bulwark contra nuestro Mailcow | JMAP-sobre-Dovecot funciona, cliente de terceros incluido — **VALIDADO 100% 2026-08-08** |
| S2 ✅ | go-imap/v2 (rama v2 pinneada) contra nuestro Dovecot: QRESYNC, CONDSTORE, IDLE, NOTIFY multi-mailbox | Base técnica del sync engine; fan-out colapsado confirmado (1 conexión, N carpetas) — **VALIDADO 2026-08-08** |
| S3 ✅ | Benchmark tsvector+GIN, 5M mensajes en el VPS real | 8/10 shapes con margen 4x-30x; 3 configs obligatorias (gin compuesto, force_custom_plan, STATISTICS 4000); Meilisearch fuera del MVP — **VALIDADO 2026-08-09** |
| S4 ✅ | Corpus de MIME patológico (110 casos) + harness dual-parser | Cascada go-message → enmime → raw blob validada; 0 panics/hangs — **VALIDADO 2026-08-09** |

## Para retomar rápido

1. `docs/adr/ADR-001-arquitectura.md` — la decisión completa (2 páginas)
2. `docs/research/00-sintesis-fase0.md` — síntesis auditada + arbitrajes + riesgos
3. Los informes 01-04 de `docs/research/` solo si necesitás el detalle de un área
