# Spec L2 — Fase 2 del servidor: escritura, envío y push (W1-W4)

> **Estado:** ACEPTADA — arbitrajes W-A1..W-A4 firmados por el director técnico bajo la delegación vigente · **Fecha:** 2026-08-12
> **Autor:** Fable 5 (director técnico) · **Nivel:** L2
> **Base:** ADR-001 §2 (fase 2) y §4 (outbox transaccional) · L2-sync-engine §4.3 (tabla `intents`, ya existente) · L2-jmap-server (núcleo J1-J4 en producción piloto) · RFC 8620 §5.3/§7 · RFC 8621 §4.6/§7
> **Hito de cierre:** Bulwark **opera** mail real a través de Moov — archivar, marcar, mover, redactar, enviar con undo — con push instantáneo al navegador. La fase 1 probó que leemos bien; la 2 prueba que escribimos sin romper ni duplicar nada.

## 1. Objetivo y no-scope

Servidor completo de lectura/escritura: `Email/set` (flags, moves, destroy), `Mailbox/set`, `EmailSubmission/set` con undo send, y push SSE (RFC 8620 §7). **No-scope:** PWA propia (L2 aparte), Sieve/quotas/snippets (fase 3), labels custom más allá de keywords estándar (la A6 completa espera a la PWA que las muestre).

## 2. Arbitrajes de diseño

**W-A1 — Orden de escritura: Dovecot primero, store después, respuesta al final.** Dovecot es la fuente de verdad (ADR): un `/set` aplica a IMAP (conexión del pool de la cuenta, `UNCHANGEDSINCE` cuando aplica), refleja en el store, y recién entonces responde `updated`. Sin estados "pendiente de sincronizar" visibles al cliente en fase 2: la latencia IMAP interna medida (~ms en red local Docker) cabe en el presupuesto <100 ms percibidos; la UI optimista es trabajo del cliente (la PWA lo hará; Bulwark hace lo suyo). Si IMAP falla → `SetError` honesto, el store no se toca. La tabla `intents` queda para lo diferido por naturaleza (envío) y como cola de reintentos de la reconciliación, no como camino primario de flags.

**W-A2 — `destroy` = mover a Trash; expunge solo desde Trash.** Un `Email/set destroy` sobre mensaje fuera de Trash ejecuta MOVE a la carpeta `\Trash` (semántica Gmail, reversible). Destroy de algo YA en Trash = STORE `\Deleted` + UID EXPUNGE real. Jamás un expunge directo desde otra carpeta — el usuario piloto es el dueño del producto y su buzón es real.

**W-A3 — Undo send server-side.** `EmailSubmission/set` encola en `intents` con `not_before = now + undoWindow` (default 10 s, config 5-30 por cuenta). La ventana permite `EmailSubmission/set destroy` (cancela limpio). Vencida la ventana, el executor del outbox envía por SMTP submission (`postfix:587`, app password — scope smtp ya provisto) con las reglas duras del ADR §4: **nunca reintentar tras un 250** (el 250 se persiste ANTES de cualquier otra acción), dedupe por Message-ID, luego APPEND a `\Sent` (fallo del APPEND = warning + reintento del APPEND solo, jamás re-envío) y `onSuccessUpdateEmail` (draft → sent, `$draft` fuera). Crash entre 250 y persistencia: el arranque reconcilia contra `\Sent` + Message-ID antes de tocar nada pendiente.

**W-A4 — Push SSE per RFC 8620 §7.3.** `eventSourceUrl` real: endpoint EventSource autenticado que emite `StateChange` (los mismos estados que J3 calcula) con `Last-Event-ID` para resume, heartbeat (comentario SSE) cada 30 s, y cierre limpio en shutdown. Fuente: un broker in-process suscripto al ciclo del watcher/incremental (el engine ya sabe cuándo cambió una cuenta); sin polling interno. Límite de conexiones SSE por cuenta (config, default 4). CORS ya resuelto en J1 aplica al endpoint.

## 3. Épicas

| # | Épica | Modelo | ACs clave |
|---|---|---|---|
| W1 | **Núcleo de escritura**: executor IMAP de writes (flags/keywords, MOVE, expunge-en-Trash) sobre `internal/imap` existente + `Email/set` (update flags/mailboxIds, destroy per W-A2) + reflejo en store + `oldState/newState` correctos | **Fable 5** (escribe en buzones reales; la corrección es no negociable) | Round-trip contra Dovecot real: set desde JMAP → visible por IMAP puro y viceversa; conflicto UNCHANGEDSINCE surfaceado como `SetError`; destroy fuera/dentro de Trash con las dos semánticas; ningún write sin cuenta autenticada dueña; suite de idempotencia (replay del mismo /set) |
| W2 | **`Mailbox/set`** (create/rename/delete vía IMAP, roles protegidos — no se borra INBOX/Trash/Sent), keywords estándar completas, enforcement del techo 26 (A6/V1) con error claro | Opus | Ciclo completo contra Dovecot real; borrar mailbox con mensajes → `mailboxHasEmail` salvo `onDestroyRemoveEmails`; la keyword 27 se rechaza con el error de A6 |
| W3 | **`EmailSubmission` + outbox + undo** per W-A3, incluyendo `Email/set create` en Drafts (upload de blob + APPEND `$draft`) como prerequisito de redactar | **Fable 5** (enviar mail duplicado o perdido es el peor fallo posible del producto) | Envío real desde `moov-test` a un buzón de prueba segundo; undo dentro de ventana cancela sin rastro; crash-recovery del outbox probado (kill entre fases); dedupe verificado; nunca-retry-tras-250 con test que lo demuestra |
| W4 | **SSE** per W-A4 + E2E Bulwark del ciclo completo + deploy actualizado del piloto | Opus | StateChange llega al EventSource <1 s tras cambio IMAP externo (medido); resume con Last-Event-ID sin pérdida; Bulwark: archivar/marcar/mover/enviar operando de verdad en navegador; piloto redeployado y sano |

Orden: W1 → W2 ∥ W3 (scopes disjuntos: W2 = mailbox ops en jmap/mail+imap; W3 = submission/outbox en paquetes nuevos) → W4. Nota fase-1: al aterrizar W1, `myRights`/`isReadOnly` de J2 pasan a decir la verdad nueva.

## 4. Contracts

- `internal/jmap/mail`: los `/set` siguen el patrón de registro existente; SetError vive en el mapa único de errores de J1 (los códigos ya están reservados).
- Executor de escritura: `internal/sync` expone `ApplyFlagChange/ApplyMove/ApplyDestroy(ctx, account, ...)` — la capa JMAP jamás toca `internal/imap` directo (misma regla de capas de siempre).
- Outbox: paquete nuevo `internal/submit` (SMTP client stdlib + estado en `intents`); `internal/jmap/mail` solo encola/cancela.
- SSE: `internal/jmaphttp` gana el endpoint; el broker vive en `internal/sync` (dueño del conocimiento "cambió la cuenta X") con interfaz `StateEvents(accountID) <-chan StateChange`.
- Config nueva: `MOOV_UNDO_WINDOW_SECONDS`, `MOOV_SSE_MAX_CONN_PER_ACCOUNT`, SMTP host/port.

## 5. Riesgos

1. Escritura en buzón real del dueño del producto — mitigado por W-A1/W-A2 (nada destructivo directo, todo reversible) y por el orden W1 primero con Fable.
2. El doble-envío es el fallo imperdonable — W3 con Fable, crash-recovery probado y la regla 250 con test.
3. Bulwark puede ejercitar `/set` con shapes que no anticipamos — mismo playbook que J4: observar tráfico real, cerrar gaps documentados.
4. SSE mantiene conexiones largas — límites por cuenta + heartbeat + prueba de shutdown limpio.

## 6. Aprobación

- [x] W-A1..W-A4 firmados bajo la delegación vigente (2026-08-09). Hito de cierre verificable por Diego en navegador, como siempre.
