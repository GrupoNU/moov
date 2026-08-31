# Gate final Gmail-class (E11) — informe del auditor independiente

> **Fecha:** 2026-08-30 · **Auditor:** agente independiente (no implementó ninguna épica) · **HEAD auditado:** `351bc1e` (árbol de trabajo limpio, verificado)
> **Contra:** `docs/specs/L3-gmail-class-plan.md` (plan firmado, E0-E11, GC-1…GC-10, D-1…D-8) y `docs/research/06-gmail-canon.md` (canon §2, §7)
> **Método:** 3 pases — cobertura del canon fila por fila (≥20 filas CORE con lectura de código y tests), decisiones/arbitrajes/costuras, y re-ejecución de las verificaciones de calidad. Cada veredicto con `archivo:línea` o nombre de test. Los dos hallazgos críticos fueron confirmados por el auditor directamente en el código, no solo por los verificadores delegados.

---

## VEREDICTO: **DEPLOYABLE-WITH-FINDINGS**

El programa es desplegable al piloto (la firma del §9 lo autoriza explícitamente) y el núcleo es sólido: **la capa de seguridad/confianza es excelente y está verificada** (invariantes GC-6/GC-7 con tests que se protegen a sí mismos, chokepoint único de sanitización auditado, particionado Sieve a prueba de escapes), la calidad mecánica está toda verde (2046/2046 tests web, build OK, Go build+vet+suite DB race-clean), y D-7 se resolvió **con números medidos**, exactamente como se firmó.

Pero el gate encuentra **dos hallazgos críticos y tres altos** que comparten una misma forma — *capacidad construida de un lado de la costura, jamás cableada del otro, con los tests escritos solo del lado que shippeó* — más una docena de menores. Ninguno rompe el correo diario ni la seguridad; dos violan claims explícitos del plan. El hallazgo 1 es un fix de una línea y **conviene aterrizarlo antes del deploy**; el resto puede ir en un follow-up con nombre.

---

## Pass 3 — Calidad re-verificada (todo verde)

| Verificación | Resultado |
|---|---|
| `npm test` (web) | **2046/2046 en 107 archivos** — la cifra exacta del claim |
| `npm run build` | OK, 3,79 s (aviso: chunk 585 kB > 500 kB — nota de perf, no bloqueante) |
| `go build ./... && go vet ./...` (VPS, contenedor CI-style) | OK (con `-buildvcs=false`; el árbol sincronizado sin VCS confiable) |
| `go test -race -count=1 -p 1 ./internal/store/... ./internal/jmap/...` | **ok** los 3 paquetes (store 105 s, jmap, jmap/mail) |
| CI (`.github/workflows/ci.yml`) | 5 jobs: build/vet/lint/test · corpus MIME · migraciones PG17 · **conformance RFC 8620/8621** · web typecheck/lint/test/build |

**Criterios ADR §6, cada uno con su historia:** búsqueda <100 ms → S3 + páginas E3 de 3-27 ms medidas (`search.go:53-71`) · acciones <100 ms → optimistic UI + 19-166 ms medidos en W-fase; la deuda de borrar carpeta tuvo fix real (cache de SELECT por conexión, `427373f`) con límite honesto auto-pinneado (`write_selection_test.go:239`) y además ya no es alcanzable desde el cliente · scroll 100k → **D-7 resuelto con números** (`internal/jmap/mail/search.go:27-110`: página constante 3-27 ms hasta profundidad 100k; el commit `fde61b9` existe para *retirar* cifras SQL que exageraban el margen 3×) · push real → SSE existente (671-942 ms medidos en W4a) · teclado completo → **con la salvedad del hallazgo 1** · undo send → `{5,10,20,30}` verificado en ambos lados · PWA instalable+offline → manifest+SW+IDB+Outbox a nivel código; la instalación real queda para el smoke.

---

## Pass 1 — Cobertura del canon, fila por fila (resumen; CORE únicamente)

| Canon | Fila | Veredicto | Evidencia |
|---|---|---|---|
| §2.1 | Vista de conversación | **SHIPPED** | `collapseThreads` servido con un solo rechazo documentado (sort relevance, `query.go:255-294`); reader canon-shaped: más nuevo abajo, bodies lazy, solo-expandidos-se-marcan-leídos (`web/src/mail/conversation.ts`, 29 tests); `;` `:` `p` `n`; recorte citado (`quotedTail.ts`); acciones por-mensaje vs por-hilo; setting on/off |
| §2.1 | **Reglas de corte de presentación (cambio de asunto, >100)** | **MISSING-SILENT** | En ningún lado del código; solo en el plan. Hallazgo 4. La ventana-de-una-semana correctamente NO adoptada (GC-3); `TestThreadingGraphBeatsSubject` pinnea el grafo-gana-al-asunto |
| §2.2 | Archivar (la respuesta vuelve al inbox) | **SHIPPED** | `actions.ts:47`; nada suprime la entrega salvo mute (verificado: `mute.go` es el único skip) |
| §2.2 | Papelera: vaciar + eliminar definitivo | **SHIPPED** | `emptyTrash.ts` (10 tests), confirmación con conteo. *Menor:* la delegación documentada de la retención 30 días a Dovecot/Mailcow no se escribió (hallazgo 12) |
| §2.2 | Snooze (GC-10, vuelve AL TOPE) | **SHIPPED** | MOVE a `Snoozed` + **re-APPEND con INTERNALDATE fresco** (decisión documentada `snooze.go:37-90` — por eso vuelve al tope y es visible en IMAP), waker (`waker.go`), `b`, `g b` (`shortcuts.ts:404,589`) |
| §2.2 | Mute (3 escotillas) | **SHIPPED con desviación** | Clave durable = Message-ID del miembro MÁS VIEJO (`82c7844`); archiva en Dovecot. Escotillas 1 y 3 sí; **la 2 (grupo) está deliberadamente AUSENTE** con razonamiento preciso en `mute.go:48-77` — hallazgo 9 |
| §2.2 | Spam / no-spam | **SHIPPED** | kinds `spam`/`notSpam` (`actions.ts:59-60`), `!` (`shortcuts.ts:465`), banner + supresión de imágenes en Junk que **le gana a la preferencia** (`ReadingPane.tsx:306`, `ReadingPane.test.tsx:437`). **La fila de gate obligatoria (Rspamd aprende del MOVE) sigue sin evidencia** — hallazgo 8, va al smoke |
| §2.2 | Bloquear remitente | **SHIPPED** | Receta Sieve (`model.go:34`), UI (`blockedSenders.ts`, 12 tests), fila del reader |
| §2.2 | Unsubscribe | **PARTIAL** | Parsing List-Unsubscribe + mailto + link (23 tests); **el POST one-click RFC 8058 server-side NO existe** — TODO honesto en `ReadingPane.tsx:887` ("a later epic"), pero el plan E2 lo exigía. Hallazgo 7 |
| §2.2 | Estrella / leído / mover | **SHIPPED** | `s`, `is:starred` bitmask real (`narrowing.go:245`); `Shift+I/U`, `_` (`shortcuts.ts:489`); `l`. **`v` (move-to) falta del mapa** — hallazgo 12 |
| §2.2 | Undo `z` | **SHIPPED** | `shortcuts.ts:468`, inverse patches + toast (`undo.test.ts`, 28 tests) |
| §2.2 | Hover exactamente 4, ON, un setting | **SHIPPED** | `MessageList.tsx:317-327` (archive/delete/toggleRead/snooze, gateadas por la única pref), default ON (`prefs.ts:133`) |
| §2.2 | Auto-advance OFF con opt-in | **SHIPPED** | pref default "list" (`prefs.ts:134`), cableada (`MailScreen.tsx:1543`) |
| §2.3 | Undo send {5,10,20,30} | **SHIPPED** | `prefs.ts:55` = `prefs.go:199`, clamp server (`config/submit.go:75`), 3 tests nombrados |
| §2.3 | Schedule send (100, cancelar→borrador) | **SHIPPED** | overQuota al 101 (`schedule_test.go:20`), `TestScheduledSendKeepsItsDraft` |
| §2.3 | Firmas múltiples con nombre + defaults nueva/respuesta | **MISSING (cliente)** | Clave v2 servida (`store/prefs.go:293`); el composer sigue con la firma única de Identity (`Composer.tsx:1290`). Parte del hallazgo 2 |
| §2.3 | Autocompletado "Other contacts" + opt-out | **SHIPPED** | Índice auto-alimentado, combobox (`AddressField.tsx:341`), fila opt-out con borrado (`strings.ts:256-258`). *El opt-out vive en localStorage, no en la clave v2* — hallazgo 2 |
| §2.3 | Extensiones bloqueadas (2 capas) | **SHIPPED** | cliente `blockedExtensions.ts:69` + server `blocked_extensions.go:35` + test de paridad entre ambas listas. Límite documentado: solo-extensión, no inspecciona archives (`blockedExtensions.ts:42-46`) |
| §2.3 | Reenviar como .eml / texto plano / Send & Archive | **SHIPPED** | `MailScreen.tsx:3040`; `ComposerE7.test.tsx:131-262` (incl. des-archivar al deshacer) |
| §2.4 | Reading pane 3 modos / densidad 3 / snippets / inbox types | **SHIPPED** | 10 secciones (`registry.ts:31-58`); 3 prefs muestreadas fluyen a comportamiento real. Faltan 2 de 18: editor reply-to/bcc y botones icono/texto — hallazgo 12 |
| §2.5 | Operadores de búsqueda | **SHIPPED con 2 huecos** | Set E3 completo incl. fechas y negación (`searchQuery.ts:29-111`); rechazo honesto y visible de los 5 diferidos con dos puntos (`searchQuery.test.ts:339`); snippets sin confiar en HTML del server (`snippet.ts:25`). Huecos: `()` se traga como texto sin aviso; sugerencias de direcciones sin conectar pese a precondición cumplida — hallazgo 12 |
| §2.6 | Labels bajo el techo 26 | **SHIPPED** | Techo explicado permanente en UI (`strings.ts:904,937`), aplicado en ambos lados, paleta cerrada de 12 con AA computado por tema, cero pickers libres, chips jamás tinte. *Desviaciones:* anidado por prefijos de keyword y no por carpetas (letra de GC-5); colores/visibilidad en localStorage con caveat en pantalla — hallazgos 2 y 12 |
| §2.7 | Teclado (mapa Gmail, event.code) | **PARTIAL — hallazgo 1** | Mapa ~40 bindings, chords `* a/n/r/u/s/t` y `g …`, `[`/`]`, 73 tests. **El resolver por `event.code` es inalcanzable en producción** (ver hallazgos). Faltan `v` y el chord `c d` |
| §2.8 | Vacation | **SHIPPED — ejemplar** | `:days 4` + `:handle` sensible a edición + guardas spam/listas/Precedence/Auto-Submitted emitidas literales + ventana UTC exacta al segundo (`generate.go:284-308`, `TestVacationEmission`). *Contacts-only rehusado con razón (`vacation.go:293`) — necesita enmienda del plan, hallazgo 12* |
| §2.9 | Notificaciones 2 modos | **SHIPPED** | `prefs.ts:86`, Notification API sobre SSE (`MailScreen.tsx:1190`), ConnectionPill (la muerte del SSE ya no es silenciosa), `recycleStaleSSE` cableado de verdad (`MailScreen.tsx:556-585`). Web Push documentado como más-allá-de-Gmail (`notify.ts:8-12`) |
| §2.10 | Offline (leer/buscar/responder + Outbox) | **SHIPPED con 1 hueco** | IndexedDB real (`idb.ts`, `cache.ts`), Outbox real + carpeta virtual (`outbox.ts`, `MailboxList.tsx:48`), búsqueda offline (`search.ts:105`), adjuntos declarados no-previsualizables. **El setting de profundidad es un placeholder "coming soon"** (`registry.ts:441`) — parte del hallazgo 2 |
| §2.11 | Ver original / cuota | **SHIPPED** | `rawMessage.ts` + copiar (tests en `ReadingPane.test.tsx`); `Quota/get` RFC 9425 + `QuotaRow.tsx` |

**Diferidos §6:** verificado que ninguno tiene UI a medias (sin superstars, sin Manage subscriptions, sin plantillas, sin VAPID — solo el comentario que lo declara diferido). Sin controles muertos hacia adelante; el problema es el inverso (hallazgo 2).

---

## Pass 2 — Decisiones firmadas y arbitrajes

| # | Firmado | Veredicto |
|---|---|---|
| D-1 PWA instalable | SÍ | **CUMPLIDA** — manifest + 5 iconos + `sw.js` (238 líneas) + `protocol_handlers` mailto (`manifest.webmanifest:41`) |
| D-2 Offline estándar | SÍ | **CUMPLIDA** — SW + IndexedDB browser-agnostic, sin nada Chrome-only |
| D-3 Teclado ON | SÍ | **CUMPLIDA** — `prefs.ts:146` default true, restatement del server |
| D-4 Imágenes forma-Gmail | SÍ | **CUMPLIDA** — display por defecto vía proxy (`prefs.ts:140`), supresión en Junk/sospechoso que le gana al setting, always/ask, verdicto Rspamd como `moov:suspicious` (`suspicious.go`) |
| D-5 Búsqueda de ajustes | SÍ | **CUMPLIDA** — `settingsSearch.ts`, 18 tests |
| D-6 TNEF | SÍ | **NO CUMPLIDA Y NO DIFERIDA** — hallazgo 3 |
| D-7 Techo con números | resolver en E3 | **CUMPLIDA con números** — 100k medido, no firmado a ciegas (`search.go:27-110`) |
| D-8 Sin mails-por-página | NO construir | **CUMPLIDA** — ausente (guard de regresión débil, solo greppea labels en inglés) |

**GC-1** iniciales (sin favicons — kill-list respetada, cero fetching de identidad externa) ✓ · **GC-2** paridad foreground ✓ · **GC-3** grafo-gana-al-asunto ✓ / reglas de presentación ✗ (hallazgo 4) · **GC-4** álgebra sin fechas ni query libre ✓ (`model.go:108-137`) · **GC-5** carpetas cargan la organización ✓ con la desviación del anidado · **GC-6** MDN jamás — invariante con test que vigila al guard (`mdn_invariant_test.go:50`, `trustInvariants.test.ts:72`) ✓ · **GC-7** URLs intactas byte a byte, sin redirector, el signer recibe el original (`urlInvariants.test.ts:55-87`) ✓ · **GC-8** nada predictivo shippeado; `is:important` rehusado por nombre ✓ · **GC-9** sin bloque IMAP/POP ✓ · **GC-10** **verificado a fondo: no existe estado de correo visible-al-usuario que viva solo en Postgres** — snooze es MOVE/APPEND IMAP real, mute usa Message-ID durables, labels son keywords IMAP ✓.

**Bonus verificado:** el bug del gauge `moov_sync_lag_seconds` (documentado 2026-08-20, "8,6 días de lag mientras entrega en segundos") **quedó corregido** durante el programa — el watcher registra `last_success_at` y el colector agrega por scope más viejo (`cmd/moovd/ops.go:160-207`, `watcher_test.go:509`).

---

## Hallazgos, rankeados

**1. CRÍTICO — El resolver `event.code` de E11 es inalcanzable en producción.** El propósito declarado del commit `930fdca` está muerto: `physicalKey()` y su tabla están bien (`shortcuts.ts:212-276`, ~60 tests cirílicos pasan), pero el ÚNICO call-site de producción — `MailScreen.tsx:3395-3403` — construye el evento con `key/ctrl/meta/alt/shift/target` y **nunca pasa `code`** (confirmado por el auditor, línea por línea). `KeyLike.code` es opcional, así que TypeScript no lo atrapa, y ningún test asserta el forwarding. En layouts no-latinos/AZERTY la app está exactamente igual de rota que antes de E11. **Fix de una línea (`code: event.code`) + un test del forwarding — recomendado ANTES del deploy.**

**2. CRÍTICO (sistémico) — Prefs v2: seis claves de servidor sin un solo consumidor cliente** (dead-control inverso, la clase de costura que este gate existía para nombrar). `adf7e7e` sirvió `labels`, `offlineDepth`, `addressAutocomplete`, `sendAndArchive`, `defaultReplyBehavior` y `signatures` (`store/prefs.go:222-293`); el `Prefs` del cliente tiene 13 claves y `parsePrefs` no parsea ninguna v2 (confirmado por el auditor por grep de cada clave en `web/src`: cero consumo de wire). Labels y opt-out de autocompletado siguen en localStorage (con caveat honesto EN PANTALLA, eso sí), `offlineDepth` es una fila "coming soon", firmas con nombre y comportamiento de respuesta no tienen ni UI ni lectura. Nada lo atrapó porque los tests de mapeo son Go↔Go y el "every preference has a row" del cliente es total solo sobre sus propias 13 claves. La promesa del commit ("el estado sigue al usuario entre dispositivos") no se cumplió para ninguna de las seis.

**3. ALTO — D-6 (TNEF) firmada SÍ, ni implementada ni diferida.** Cero rastro en `internal/parser/` ni en `web/src` (solo docs y el corpus). No está en §6. Es exactamente la clase MISSING-SILENT que P4 prohíbe — una decisión firmada por el dueño que se cayó del programa sin nombre.

**4. ALTO — Las reglas de corte de presentación de GC-3 (cambio de asunto, >100 mensajes) no existen** en ningún lado del código y tampoco en §6. Impacto práctico hoy bajo (el hilo mayor del piloto tiene 24 mensajes; el grafo mantiene juntos los hilos renombrados — comportamiento defendible pero distinto del firmado). Necesita: implementarse o diferirse con nombre.

**5. ALTO — El claim de E0 "CI verifica que Bulwark ignora la capability" no tiene check ejecutable.** Ninguno de los 5 jobs de CI toca Bulwark; el test más cercano (`prefs_test.go:183`) asserta la propiedad INVERSA. Claim de documentación sin oráculo.

**6. MEDIO — La suite e2e Playwright de E11 no existe** (cero `*.spec.ts`, sin config, sin job) y `web/README.md:496-497` afirma una verificación de focus-trap "en navegador" que descansa en una sesión ad-hoc irreproducible (artefactos no versionados en `.playwright-mcp/`). El CI es honesto al respecto (`ci.yml:218`); el README no. Además el focus-trap usa `<dialog>.showModal()` nativo (correcto) pero todos los suites lo stubean.

**7. MEDIO — RFC 8058 one-click sin lado servidor.** El plan E2 lo exigía como "trabajo de servidor, no solo parsing"; se shippeó el parsing + el link honesto con un TODO que lo pospone a "a later epic" sin fila en §6 (`ReadingPane.tsx:887`).

**8. MEDIO — La "fila de gate obligatoria" de E2 (el MOVE a Junk dispara el aprendizaje de Rspamd vía imapsieve) sigue siendo un supuesto sin evidencia** en el repo. Solo verificable en el VPS → primer ítem del smoke post-deploy.

**9. MEDIO — Mute: la escotilla 2 de Gmail ("enviado a un grupo tuyo") está deliberadamente ausente**, con razonamiento técnico sólido en `mute.go:48-77` (sin membresía de grupos, List-Id sería un bypass silencioso del mute), pero es una desviación del plan (que prometía "las 3 excepciones exactas") registrada solo a nivel código — P4/§3 pedían arbitraje visible. El comportamiento es acotado y la escotilla 3 subsume el caso común.

**10. MEDIO — El confinamiento de `internal/sieve` es solo prosa.** `doc.go:6` reclama paridad con `internal/imap`, que se aplica dos veces (depguard + architecture test); sieve no tiene ni lo uno ni lo otro, y `sieve.Client` se consume directo en `sieve_adapter.go` y `cmd/moovd/sieve.go`. Mitigante: los tipos de wire son unexported (fuga de forma, no de formato).

**11. MENOR — Los informes de gate P2 por épica no están versionados en el repo** (se entregaron como reportes de agentes al director; `docs/reports/` no existía hasta este informe). La disciplina se juzgó sobre la evidencia visible — código+tests+canon — y en general se sostiene, pero el plan pedía "informe con las tres columnas" y ese artefacto no quedó. Registrarlo como deuda de documentación.

**12. MENORES (lista honesta):** faltan del panel el editor reply-to/bcc y botones icono/texto (16/18 de E5) · teclas `v` (move-to — pese a que `shortcuts.ts:70` cita el canon) y chord `c d` (inalcanzable, sin estado `pendingC`) · vacation contacts-only rehusado con razón pero listado como entregable en el plan (enmienda pendiente) · la delegación documentada de retención 30 días de papelera no se escribió · `()` en búsqueda se traga como texto sin rechazo visible · sugerencias de direcciones sin conectar al índice E7 (precondición ya cumplida) · anidado de labels por prefijos de keyword, no por carpetas (letra de GC-5) · `SettingsDialog.test.tsx:575-599` quedó obsoleto y hoy pinnea el fallback, no el producto · comentario stale en `register.go:234` ("only ever declines" — Thread/changes es real desde `b315840`, con 5 tests) · un OR colapsado puede listar una conversación dos veces (auto-declarado, servido a propósito, `adapter_query.go:259`) · `QuotedHtml` atómico shippeó como `QuotedTailSplit` con invariante por test, no por tipo (mecanismo sano, artefacto distinto del nombrado) · chunk JS de 585 kB sin code-splitting.

**Lo destacable (para que el balance sea justo):** el trío de confianza E10 (MDN/URLs/chokepoint) tiene tests-invariante que vigilan a sus propios guards; el particionado Sieve con CHECKSCRIPT-antes-de-PUTSCRIPT, preservación byte-a-byte de scripts ajenos y reenvío gateado en tres puntos independientes con scanner fail-closed es el mejor trabajo del programa; snooze eligió re-APPEND sobre MOVE con la decisión documentada donde importa; D-7 retiró números que lo favorecían; y el rechazo de `is:important` y de contacts-only muestra la disciplina de "restringir, no fingir" funcionando.

---

## Guion de prueba del dueño (post-deploy, en orden — cada ítem: hacé X, deberías ver Y)

Precondición: deploy del piloto + VPN. Entrá por `https://moov.atmosfera.cloud` (o `mail.gruponu.com`).

1. **Logueate.** Deberías ver el inbox como CONVERSACIONES: filas con contador de mensajes por hilo, no mensajes sueltos.
2. **Abrí un hilo largo** (tenés uno de 24). Los mensajes viejos colapsados, el último abierto; expandí uno del medio y tocá "mostrar contenido recortado" — la cita aparece sin romper el formato.
3. **Mandate un mail desde otra cuenta.** Debería aparecer solo, sin refrescar (SSE) — y si activaste notificaciones en Ajustes, con notificación del navegador (pestaña abierta).
4. **Archivá con `e` y deshacé con `z`.** El mail vuelve al inbox con el toast de deshacer.
5. **Posponé un mail 10 minutos (`b`).** Desaparece del inbox y está en Pospuestos (`g b`). Abrí SOGo en paralelo: el mail se movió DE VERDAD a la carpeta Snoozed. Al vencer, vuelve ARRIBA del inbox.
6. **Silenciá un hilo (`m`) y pedí que te respondan.** La respuesta va directo a Archivo, sin pisar el inbox. Si te ponen explícitamente en To o Cc, esa SÍ entra. (Ojo: mail de lista/grupo sin vos en To/Cc se archiva — es la desviación conocida del hallazgo 9.)
7. **Programá un envío para dentro de 15 min y cancelalo.** Vuelve a Borradores y NUNCA sale (verificá que al destinatario no le llegó nada).
8. **Respondé con "Enviar y archivar".** El hilo se archiva y hay EXACTAMENTE UNA copia en Enviados.
9. **Marcá spam (`!`) un mail de prueba.** Va a Junk con banner y SIN imágenes remotas. Después pedile al operador confirmar en los logs de Rspamd que ese MOVE disparó el aprendizaje — es el supuesto nunca probado (hallazgo 8).
10. **Buscá** `from:<alguien> has:attachment newer_than:7d`. Resultados correctos, con resaltado en los snippets. Probá también `is:starred` y `in:anywhere`.
11. **Creá un filtro** (de cierto remitente → mover a una carpeta) y mandá un mail que matchee: llega ya filtrado. Después abrí SOGo → verificá que tus scripts Sieve preexistentes siguen INTACTOS.
12. **Configurá un reenvío a una dirección externa.** Debe llegar un mail de verificación con token a esa dirección, y el reenvío NO funciona hasta aceptarlo.
13. **Activá vacaciones desde hoy.** Escribite desde otra cuenta: llega la auto-respuesta. Escribí de nuevo el mismo día: NO llega otra (throttle 4 días). El banner "Finalizar ahora" está visible arriba.
14. **Instalá la PWA** (icono de instalar del navegador) y después cortá la red: podés leer lo ya visto, buscar, y responder — la respuesta queda en Bandeja de salida y sale sola al volver la conexión.
15. **Etiquetas:** creá 2-3 con la paleta, verificá los chips en las filas (nunca la fila teñida) y que la UI te explica el techo de 26 al acercarte.

*Nota:* el atajo de teclado en layouts no-latinos NO va a andar hasta el fix del hallazgo 1. Y sigue pendiente de siempre el clic visual sobre un mail en Bulwark (limitación del harness desde J4, verificado server-side).

---

## Qué verifiqué y qué tomé en fe

**Verificado directamente por el auditor (código leído, comandos corridos):** las 4 verificaciones de calidad · árbol limpio en `351bc1e` · ausencia total de TNEF · números de D-7 · valores de undo send en ambos lados · emisión Sieve de vacaciones (guardas literales) · las escotillas del mute y su ausencia documentada · teclado ON por defecto · manifest/SW/offline.html presentes · gramática de operadores · techo 26 en strings de UI · `collapseThreads` servido · schema de verificación de reenvío (0010) · hover 4-y-solo-4 gateadas · print/`[`/`]`/`_`/chords/props del reader · kinds de spam + `!` · fix del gauge de sync-lag · **los dos críticos re-confirmados línea por línea** (call-site sin `code`; cero consumo cliente de las 6 claves v2).

**Verificado por los verificadores delegados, con spot-checks míos en lo load-bearing:** invariantes GC-6/GC-7 y el chokepoint · particionado Sieve y sus tres gates de reenvío · Thread/changes real + fix de borrar carpeta · cc/bcc indexados (trigram 293×) · paleta AA computada · wiring de density/snippets/auto-advance · Outbox/IDB por dentro.

**Tomado en fe / no verificable sin navegador ni credenciales (por eso existe el guion de arriba):** todo comportamiento runtime en el VPS — instalación PWA, SSE/notificaciones en vivo, offline real, el aprendizaje de Rspamd, el round-trip del token de reenvío, el timing del waker, los números históricos de latencia de las fases W (citados de docs del repo), y el contenido de los gates P2 por épica (entregados como reportes al director, no versionados — hallazgo 11).
