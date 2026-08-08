# Spike S2 — go-imap/v2 contra nuestro Dovecot: QRESYNC, CONDSTORE, IDLE, NOTIFY

> **Fecha:** 2026-08-08 · **Resultado: ✅ VALIDADO — con hallazgos que cambian el plan de la librería**
> Objetivo: validar empíricamente la base técnica del sync engine (Go + go-imap/v2) contra el Dovecot 2.3.21.1 real de nuestro Mailcow, antes de escribir código de producto.
> Datos crudos, transcripciones de protocolo y código: `spikes/s2-goimap/` (RESULTS.md).

## Veredicto por test

| Test | Veredicto | Resultado en una línea |
|---|---|---|
| QRESYNC raw (sin librería) | ✅ PASS | El QRESYNC de nuestro Dovecot es de manual: `VANISHED (EARLIER)`, replay de flags con MODSEQ |
| Capabilities | ✅ PASS | CONDSTORE/QRESYNC/IDLE/NOTIFY anunciadas; sin OBJECTID ni IMAP4rev2 |
| CONDSTORE (go-imap beta.8) | ✅ PASS | `CHANGEDSINCE` aísla exactamente lo cambiado; caveat H6 |
| IDLE | ✅ PASS | Datos unilaterales en ~0,5 s (mismo host, cota superior informal) |
| NOTIFY multi-mailbox | ✅ PASS | **1 conexión observó 3 mailboxes distintas — el fan-out colapsa** (tesis central de S2) |
| QRESYNC vía librería | ⚠️ FINDING | go-imap se niega a `ENABLE QRESYNC` (allowlist cliente) — resuelto por H3 |
| Control NOTIFY raw | ✅ PASS | Exonera a Dovecot; los límites son todos de la librería |
| Patch PR #757 | ✅ VALIDADO | Aplica limpio sobre tip de rama v2 y funciona end-to-end contra nuestro server |

**Titular: todas las limitaciones encontradas son del cliente (go-imap). Dovecot se comportó correctamente en el 100% de los tests, incluso en el que el research esperaba que fallara.**

## Hallazgos de ingeniería (insumos para el sync engine)

| # | Hallazgo | Implicancia para Moov |
|---|---|---|
| H1 | **QRESYNC de Dovecot validado a nivel protocolo**: reconexión con `SELECT (QRESYNC (uidvalidity modseq))` devuelve exactamente el delta (expunges como `VANISHED (EARLIER)`, cambios de flags con MODSEQ); HIGHESTMODSEQ monotónico en cada mutación | La base del resync incremental del engine es sólida en el servidor. Cualquier problema de QRESYNC de acá en más es problema del cliente |
| H2 | **go-imap/v2 beta.8 no tiene QRESYNC** (el allowlist de `Enable()` lo rechaza antes de tocar la red, sin escape hatch raw) **ni NOTIFY** (mergeado a rama `v2` 7 días después del tag). Pin usado: `v2.0.0-beta.8.0.20260702120225-f68ef419e622`. Ojo: `@v2` no resuelve (el nombre de rama colisiona con el sufijo de major version) — pinnear por commit | El engine se construye sobre la **rama v2 pinneada por commit exacto**, vendorizada. No esperar v2.0.0 estable antes de shippear |
| H3 | **PR #757 (QRESYNC cliente) validado end-to-end**: aplica limpio sobre el tip de rama v2, cubre TODO el camino (`Enable`, `SELECT (QRESYNC …)`, `FETCH … VANISHED`, handler `Vanished`) — ~118 líneas de código de producción + 206 de tests. Corrido contra nuestro Dovecot: 0 fallas, UIDs vanished y modseqs correctos | Adoptar como **patch vendorizado** (decisión de mantenimiento, no proyecto de ingeniería). Re-validar el patch en cada bump del pin |
| H4 | **Dos bugs del encoder NOTIFY de go-imap**: `Status:true` emite `SET (STATUS) (…)` en vez de `SET STATUS (…)`, y `Mailboxes` explícitas omiten la keyword `MAILBOXES` — ambos rechazados `BAD` por Dovecot. Los unit tests upstream asertan los bytes incorrectos (suite verde, comando inválido) | Fix del encoder = **prerequisito de corrección** (ver H5b). Candidato ideal a contribución upstream. Evidencia de que go-imap NOTIFY nunca se probó contra un servidor real |
| H5 | **Dovecot NO viola RFC 5465** (refuta la sospecha del research): con la sintaxis correcta `NOTIFY SET STATUS (…)`, el STATUS inducido incluye `HIGHESTMODSEQ` 3/3. La observación de omisión era artefacto del bug H4 | Con el encoder arreglado, el engine puede confiar en el HIGHESTMODSEQ de los STATUS de NOTIFY |
| H5b | **Pérdida silenciosa de flags con el NOTIFY degradado**: en la única forma que go-imap emite hoy (`SET (PERSONAL …)` sin STATUS), un cambio puro de flags en carpeta no seleccionada **no produce notificación alguna** (`MESSAGES`/`UNSEEN` no cambian y no llega HIGHESTMODSEQ) | Sin el fix H4, los cambios de flags hechos por otros clientes se pierden hasta el próximo poll — bug de corrección invisible en testing normal |
| H6 | **`Store()` con `UNCHANGEDSINCE` es silencioso ante conflicto**: el servidor rechaza bien el write (`[MODIFIED …]`) pero la librería devuelve `err=nil` y 0 respuestas — éxito y rechazo son indistinguibles | Toda escritura condicional del engine **debe verificarse con read-back** (o parchear la librería para exponer `MODIFIED`). Riesgo de corrupción de flags bajo concurrencia |
| H7 | **NOTIFY es notification-only en Dovecot**: rechaza `MessageNew (fetch-att)`; en carpetas no seleccionadas todo evento llega como un `STATUS` sin tipo de evento | Siempre hay un FETCH de follow-up: batchearlo por mailbox notificada, no reaccionar por evento. El cliente diffea contra su propio estado |
| H8 | **Sin OBJECTID (RFC 8474)** en Dovecot 2.3 | Los IDs estables de mensaje/thread (JMAP `Email/id`, `threadId`) los fabrica nuestro store: UIDVALIDITY+UID + content hash para sobrevivir moves. Threading propio |
| H9 | **El watcher NOTIFY debe vivir en IDLE**: go-imap solo lee el socket con un comando en vuelo o en IDLE; y `NOTIFICATIONOVERFLOW` existe | Loop de mantenimiento de IDLE explícito (auto-restart de 28 min de la librería) + fallback a resync completo ante overflow. Reconciliación periódica defensiva igual |

## Sintaxis NOTIFY probadas a mano contra Dovecot (referencia)

| Comando | Resultado |
|---|---|
| `NOTIFY SET STATUS (PERSONAL (…))` | OK |
| `NOTIFY SET (PERSONAL (…))` | OK (forma degradada — ver H5b) |
| `NOTIFY SET (MAILBOXES (INBOX "S2/folder1") (…))` | OK |
| `NOTIFY SET STATUS (SELECTED (…)) (PERSONAL (…))` | OK |
| `NOTIFY SET (PERSONAL (MessageNew (UID FLAGS …) …))` | BAD — fetch-att no soportado (H7) |

## Arbitraje de dirección (A4)

**Decisión:** el sync engine se construye sobre go-imap/v2 **rama v2 pinneada por commit, vendorizada y encapsulada tras `internal/imap`**, con un patch set propio mínimo: (1) PR #757 (QRESYNC), (2) fix del encoder NOTIFY (H4), (3) exponer `MODIFIED` (H6). Los tres son chicos, están validados o acotados por este spike, y son candidatos a contribución upstream para achicar el patch set con el tiempo.

Alternativas descartadas: esperar releases upstream (sin señales de v2.0.0; regla 2 exige avanzar con lo más potente hoy) y escribir cliente IMAP propio (costo desproporcionado; el 95% de go-imap funciona y está bien tipado). La regla de ADR-001 de vendorizar+encapsular pasa de higiene a **necesidad estructural demostrada**: la librería shippea tests que asertan bytes que un Dovecot real rechaza.

## Entorno y reproducción

- Mailcow productivo, Dovecot 2.3.21.1, red `mailcowdockerized_mailcow-network`, buzón `moov-test@atmosfera.cloud` (credenciales en `credentials/` de VPS_Mail, git-ignored; password solo por env var `IMAP_PASSWORD`).
- `mailbox_list_index = yes` confirmado (default de Dovecot 2.3, sin overrides de Mailcow) — requisito de NOTIFY.
- Código del spike y instrucciones: `spikes/s2-goimap/` (runner Docker en el VPS mail; copias de referencia en `/root/moov-s2*`).

## Próximos spikes

- **S3:** benchmark `tsvector`+GIN con corpus sintético de 5M mensajes (¿Meilisearch en MVP o fase 2?).
- **S4:** corpus de MIME patológico (prerequisito del parser).
