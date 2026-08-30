# Plan L3 v3 — Gmail-class, con Gmail como único criterio

> **Estado:** v3 — REEMPLAZA íntegramente a la v2. Redactado por el director técnico (Fable 5) sobre el canon verificado `docs/research/06-gmail-canon.md`; **verificación adversarial ejecutada e incorporada (§8)**; pendiente solo de la firma de Diego (§9).
> **Fecha:** 2026-08-30
> **Origen del reemplazo:** la v2 tomó a Bulwark como "piso a superar" y elevó su superficie (26 pestañas, 20+ mecanismos) a requisito. Se rompió en el primer contraste real (los favicons: Bulwark muestra logos que Gmail decidió NO mostrar, y con razón). Mandato del dueño: *"Volvemos al origen: GMAIL CLASS."* Gmail define QUÉ existe y POR QUÉ; Bulwark, cuando coincide, aporta solo CÓMO.
> **Evidencia:** todo ítem de este plan cita al canon (`06-gmail-canon.md`, §), que a su vez cita documentación de Google recuperada en vivo el 2026-08-30. Lo no-sourceable está en el registro §5 del canon y no funda ninguna decisión.

---

## 1. Qué invalida a la v2 y qué se conserva

**Invalidado:**
- El criterio "Bulwark como piso": ~10 features de Bulwark **fallan el filtro Gmail** con razón citable (canon §4.2) — favicons, debug tab, mini-juego, tinte de filas, iconos por carpeta, plantillas de nombre de archivo, acciones post-export, setting de delimitador, toggle de acuse de lectura, plugins/CSS inyectado. La v2 los traía o los dejaba entrar por deriva.
- La priorización de la v2: dejaba **snooze y mute como diferidos** cuando en Gmail son CORE (canon §2.2), y trataba Web Push como brecha de paridad cuando **Gmail web solo notifica con la pestaña abierta** (canon §2.9) — la paridad es Notification API sobre nuestro SSE, que ya existe.
- La estimación por suma de filas (12-15 semanas) sin calibrar contra el antecedente real.

**Conservado (con nueva justificación):**
- La persistencia de preferencias **en el servidor** vía JMAP (GA-1 de la v2): los ajustes de Gmail son account-level. Mecanismo nuestro (PostgreSQL), jamás el localStorage de Bulwark.
- La fundación ManageSieve compartida para filtros+vacaciones (GA-3): correcta y ahora reforzada — el modelo de filtros de Gmail (álgebra de labels + forward verificado) mapea 1:1 a Sieve, y su responder ES Sieve `vacation :days 4` (canon §2.8).
- Los **mecanismos** de Bulwark del 05 §5.0 (JSON-Pointer, `batched()`, `coalesceRefresh`, `QuotedHtml` atómico, etc.): sobreviven como CÓMO, cada uno anclado a la épica Gmail-mandada que dispara su clase de bug (regla de orden de G3 v2, conservada).
- Lo que ya hacemos mejor que ambos referentes se defiende (optimistic UI con rollback real, threading server-side, FTS propio, sanitizador de 3 capas + proxy HMAC, `Email/changes`): ninguna épica puede degradarlo.

**Hallazgo nuevo que reordena el plan** (canon §6.9, verificado por el director en el código): el rechazo de `collapseThreads` (`query.go:146-149`) cita "no hay índice de threads" — **obsoleto desde la migración 0004**. La lista de conversaciones está a una decisión de repertorio del store (bendecida por la disciplina S3), no a una migración.

## 2. Principios (gobiernan cada épica; el que faltó la primera vez es P2)

**P1 — El filtro Gmail es obligatorio por ítem** (canon §1): ¿Gmail lo hace? Si no, ¿por qué no? (trust / privacidad / consentimiento / disciplina de superficie). Divergencia solo si la razón es artefacto de negocio de Google, siempre arbitrada y firmada. La memoria no es fuente; lo UNSOURCED no funda decisiones.

**P2 — Gate doble de cierre por entrega, ANTES de darse por buena.** (a) Contraste contra el canon Gmail, fila por fila, con la cita; (b) contraste contra Bulwark en sesión real (Playwright autenticado contra el piloto), mismos flujos; (c) uso del dueño cuando haya deploy autorizado. Informe con las tres columnas. Ningún criterio del ADR §6 sin fila con número. *Este paso ausente causó el problema original.*

*Prerrequisitos operativos del gate (la última pasada de research falló exactamente acá — 05 §0.2):* (1) credenciales del buzón de prueba disponibles al ejecutor del gate **solo por env vars** (`MOOV_TEST_USER`/`MOOV_TEST_PASSWORD`) — provisiona Diego; (2) el deploy de Bulwark se mantiene vivo y ruteado durante todo el proyecto (y su actualización 1.8.1→1.9.2, con advisory publicado, es tarea de operación previa al primer gate); (3) acceso VPN del ejecutor; (4) el lado Gmail del contraste es contra el canon citado (documentación) — sesión Gmail en vivo solo si Diego la presta; se declara cuál de las dos se usó en cada informe.

**P3 — Estimación calibrada, no sumada.** Antecedente: motor + servidor JMAP + escrituras + PWA base = ~3 semanas reales (2026-08-07 → 2026-08-26, jerarquía de agentes de este proyecto). Cada épica se estima por analogía con épicas YA ejecutadas de este repo ("como W2", "como E3"), se re-estima al abrirla, y toda cifra que supere el antecedente en orden de magnitud se revisa a sí misma primero.

**P4 — Sin huecos silenciosos.** Lo diferido está en §6 con nombre, tamaño y razón; las divergencias en §3 con arbitraje. Nada sale del plan por omisión. La UI jamás muestra un control que no hace nada.

**P5 — El test de migración.** Google mató Inbox, Basic HTML y Labs enteros; solo migró lo probado (canon §4.1.8). Toda épica responde: ¿Gmail migraría esto? Si la respuesta es no, no entra.

## 3. Arbitrajes del director y decisiones que requieren firma de Diego

Arbitrajes ya tomados por el director (regla 2 del proyecto), registrados para que ninguna comparación futura los reabra:

**GC-1 — Identidad de remitente: iniciales.** Ya decidido por Diego (v2 §5 bis); el canon lo confirma como política Gmail (BIMI-only). Definitivo.

**GC-2 — Notificaciones: la paridad es foreground, con DOS modos pre-IA.** Gmail web notifica solo con sesión abierta (canon §2.9). Entra: Notification API sobre el SSE existente [S]. El modo intermedio de Gmail ("solo correo importante") depende del clasificador de importancia, diferido a la fase IA — shippearlo sin clasificador sería un control que no hace nada (P4). Adaptación registrada: modos "correo nuevo" / "desactivadas" ahora; el tercero llega con la fase IA. Web Push/VAPID pasa a §6 como diferido más-allá-de-Gmail, no como brecha.

**GC-3 — Threading: conservamos JWZ por References.** Nuestro agrupado (References bidireccional + subject normalizado) es superconjunto de los criterios de Gmail; las reglas de corte de Gmail (cambio de asunto, >100 mensajes) se aplican como reglas de **presentación**; la ventana de una semana de Gmail es un heurístico de su agrupado por asunto que NO adoptamos (el grafo de References es más correcto). Diferencia deliberada, documentada.

**GC-4 — Filtros: álgebra de labels sobre Sieve, restringida con honestidad.** Criterios {from, to (incl. cc/bcc), subject, hasAttachment, size}; acciones {mover/etiquetar, marcar leído, destacar, borrar, nunca-spam, forward a dirección VERIFICADA}. Sin criterio de fecha (como Gmail); sin `query` libre (sin equivalente Sieve — se restringe, no se finge). El flujo de verificación de reenvío se copia como diseño de seguridad (token por mail, pending→accepted).

**GC-5 — Labels bajo el techo 26: las carpetas cargan la organización.** Gmail puede hacer "todo es un label" porque tiene 10.000; Maildir nos da 26 keywords durables por mailbox (medido, y los semi-sistema consumen del mismo cupo). Modelo: carpetas ilimitadas para organizar (con anidado = "Nest label under"), keywords para los pocos flags transversales; paleta cerrada estilo Gmail (contraste garantizado); `labelShowIfUnread` adoptado; techo explicado en la UI, jamás escondido.

**GC-6 — MDN:** jamás auto-responder `Disposition-Notification-To`, jamás solicitar por defecto. **GC-7 — URLs intactas:** jamás reescribir links (invariante con test). **GC-8 — IA:** todo lo predictivo va detrás de un toggle maestro de consentimiento, opt-in, fase IA (orden ya fijado por Diego: PWA → IA → módulos). **GC-9 — El bloque IMAP/POP de Gmail no se construye** (error de categoría: Dovecot ES el servidor IMAP).

**GC-10 — Snooze y mute respetan "Dovecot es la fuente de verdad".** Estado que solo vive en PostgreSQL viola la invariante (otros clientes IMAP seguirían viendo el mail en INBOX; un rebuild del cache lo perdería; los `thread_id` del store no sobreviven una reconstrucción). Diseño fijado: **snooze es visible en IMAP** — MOVE a una carpeta dedicada (`Snoozed`) y MOVE de vuelta al vencer, ejecutado por el engine; el timer en el store es reconstruible desde la carpeta + metadata durable. **Mute es estado Moov-side pero con clave durable** (el grafo de Message-ID del hilo, jamás ids del store), y su efecto — "las respuestas saltan el inbox" — lo ejecuta el engine archivando en Dovecot al llegar mail del hilo muteado, con las 3 excepciones de Gmail evaluadas antes de archivar. Consecuencias asumidas y a la vista: otros clientes ven el mail archivado (coherente), y un rebuild conserva mutes (tabla con claves durables).

**Decisiones que firman con el plan (D-1…D-8)** — cada una con recomendación del director:

| # | Decisión | Recomendación | Razón |
|---|---|---|---|
| D-1 | PWA instalable (Gmail no lo es) | **SÍ** — divergencia sancionada | Artefacto de negocio de Google; criterio ADR §6 |
| D-2 | Offline browser-agnostic (Gmail: solo Chrome) | **SÍ** — divergencia sancionada | Ídem; SW+IndexedDB estándar |
| D-3 | Teclado por defecto: Gmail OFF | **ON** (divergencia registrada) | Nuestro público objetivo y regla 2 (Superhuman ON); razón de Gmail no documentada |
| D-4 | Imágenes remotas: adoptar la forma Gmail (display por defecto GRACIAS al proxy + supresión en sospechosos + setting always/ask) | **SÍ** | El proxy HMAC ya existe; es la precondición y la tenemos (canon §7.1) |
| D-5 | Búsqueda de ajustes (Gmail no la tiene) | **SÍ** | Barata, cero costo de confianza; ausencia de Gmail sin razón documentada |
| D-6 | TNEF/winmail.dat (Gmail no extrae) | **SÍ** — como sub-ítem [M] de E2, en la cascada `internal/parser` (épica E4 del L2 del sync engine) con corpus+fuzzing | Audiencia Mailcow = Outlook-pesada; cae en nuestra cascada de parser con disciplina de corpus |
| D-7 | Techo de scroll/búsqueda vs 26.869 reales y ADR §6 (100k) | Resolver en E3 con números (keyset profundo) o firmar el techo | Riesgo 6 de la v2, sigue vivo |
| D-8 | Setting "mails por página" | **NO** (lista virtualizada lo vuelve irrelevante) | Artefacto de paginación legacy |

## 4. El proyecto — un solo proyecto, once épicas, ejecutable de una vez

Tallas: XS ≤ ½ d · S 1-2 d · M 3-5 d · L 1-2 sem · XL > 2 sem (juicio calibrado P3, re-estimado al abrir). Dependencias explícitas; todo lo demás paraleliza. Cada épica cierra con su gate P2. Modelos por jerarquía del proyecto: Fable 5 en E6/E10 (protocolo/seguridad/criterio), Opus en el resto.

### E0 — Fundación de preferencias (servidor) [M] — sin dependencias
Tabla de preferencias per-account (JSONB versionado con cadena de migración), métodos JMAP get/set bajo capability vendor (`https://moov.email/ns/prefs`), cursor de estado, merge per-account, CI verifica que Bulwark ignora la capability. *(GA-1 v2, conservada.)*

### E1 — Conversación [L] — dep.: E1-servidor → E1-UI
- **Servidor [M]:** retirar el check obsoleto de `collapseThreads` con el repertorio de store que la disciplina S3 bendiga (ventana acotada, sin trabajo ilimitado); registrar `Mailbox/query` (conformidad barata); decidir `Thread/changes` (registrar o skip documentado); **atacar la deuda de borrar carpeta (1,8-6,1 s: interacción DELETE/reconciler)** — si el fix IMAP no alcanza, UX honesta.
- **UI [L]:** lista de conversaciones colapsadas (fila = hilo, contador); lector expandible con mensajes colapsados, "mostrar contenido recortado" (mecanismo `QuotedHtml` atómico), acciones por mensaje vs por hilo (con `batched()` — el hilo real más grande tiene 24 mensajes), teclas `;`/`:`/`p`/`n`, setting on/off (E0). Reglas de corte GC-3.
- Canon: §2.1. Gate: lado a lado con Gmail (comportamiento) y Bulwark (mecanismo).

### E2 — Triage completo [M-L] — dep.: ninguna (estrella/mover/lector ya tienen servidor)
- Spam/no-spam: **new-build, no plomería de props** (no existe action kind de spam en la PWA — canon §6.10 corregido): acción + `!` + MOVE a Junk, banner + supresión de imágenes en Junk (forma Gmail canon §4.1.9). **Fila de gate obligatoria:** verificar que el MOVE a Junk vía nuestra sesión app-password efectivamente dispara el aprendizaje de Rspamd (imapsieve de Mailcow) — hoy es un supuesto plausible, nunca probado desde nuestro stack.
- **Unsubscribe [S-M]:** parsing de `List-Unsubscribe`/`List-ID`, botón junto al remitente con fallback de list-ID; camino `mailto:` y **one-click HTTP POST (RFC 8058)** — trabajo de servidor, no solo parsing. "Manage subscriptions" queda diferido con nombre (§6).
- **Papelera:** botón "Vaciar papelera ahora" + retención 30 días **delegada a la política de expunge de Dovecot/Mailcow, documentada** (no un timer nuestro); "Eliminar definitivamente" ya existe.
- **Undo de acciones `z`** (las inverse patches existen — toast + timer); hover actions exactamente 4, ON por defecto, con su setting; auto-advance OFF por defecto con opt-in de 3 opciones (semántica sourceada solo en el artículo Android — se registra).
- Completar el lector: estrella, mover, marcar-no-leído (*estas tres funciones existen en `MailScreen` y no se pasan como props*), imprimir, ver-original con headers y copiar, siguiente/anterior real; `[`/`]`; chords `* a/n/r/u/s/t`; `_`; arreglar el footnote obsoleto del diálogo de atajos. Mecanismo `retainedInViewIds` con la primera vista autofiltrante.
- **TNEF (si D-6 firma) [M]:** extracción de `winmail.dat` en la cascada `internal/parser`, con corpus + fuzzing (disciplina S4/E4 del sync engine).

### E3 — Búsqueda Gmail [L] — dep.: ninguna dura; SearchSnippet tras E1 servidor
- **Servidor [M-L]:** filtros `cc`/`hasAttachment` (columnas `cc_addrs` y `has_attachments` ya existen; **`bcc` vive solo en el JSONB de addresses — predicado JSONB o migración, presupuestado acá**), `is:starred` (predicado bitmask), `OR`/`NOT`, size, exclusión por defecto de Spam/Trash + `in:anywhere`; `SearchSnippet/get` (RFC 8621 §5 — ni Gmail-web lo expone como API ni Bulwark lo llama: acá superamos a ambos); **D-7** (techo vs 26.869) se resuelve acá o se firma.
- **UI [M]:** parser de operadores (canon §2.5: `from: to: cc: bcc: subject: has:attachment is:* in:* before:/after:/older_than:/newer_than: OR - "" ()`), panel de opciones + chips, sugerencias (búsquedas recientes primero; las de direcciones cuando aterrice el índice de E7 — dependencia blanda declarada), resaltado por SearchSnippet. Operadores fuera de este set (`deliveredto: AROUND +word {} list: filename: rfc822msgid: header:`, superstars) quedan diferidos con nombre (§6).

### E4 — Snooze, mute, schedule send [L] — dep.: E0 (settings)
Los tres son CORE en Gmail y la v2 los difería — este es el mayor reordenamiento Gmail-first. Diseño de persistencia fijado en **GC-10** (Dovecot fuente de verdad; nada de estado solo-Postgres).
- **Snooze [M-L]:** MOVE a carpeta `Snoozed` + MOVE de vuelta **al tope del inbox** al vencer, ejecutado por el engine (GC-10); vista `in:snoozed`, `b`, `g b`. API: objeto vendor documentado.
- **Mute [M]:** estado con clave durable (grafo de Message-ID, GC-10); el engine archiva al llegar mail del hilo muteado, evaluando antes las **3 excepciones exactas** de Gmail (solo-a-mí / grupo / To-Cc); `is:muted`, `m`.
- **Schedule send [M]:** `sendAt` futuro sobre `intents` (la tabla existe), tope 100, **cancelar → borrador**, carpeta Programados.

### E5 — La superficie de ajustes [M-L] — dep.: E0
El panel completo con la IA de Gmail adaptada (General / Etiquetas / Recibidos / Cuenta / Filtros / Reenvío / Offline / Apariencia), cada fila del bloque DIRECT del canon §3 con sus valores exactos: undo {5,10,20,30}, imágenes always/ask (D-4), vista de conversación, hover, auto-advance, densidad (3), snippets, notificaciones (2 modos pre-IA, GC-2), **panel de lectura: sin dividir / a la derecha / abajo** (canon §2.4), **tipo de inbox: Default + "No leídos primero" + "Destacados primero"** (los determinísticos; Priority/categorías son fase IA), idioma (el switcher que falta), teclado on/off (D-3), firma, reply-to/bcc, tema, botones icono/texto. Búsqueda de ajustes si D-5 firma [S]. Nada de IMAP/POP (GC-9). Esqueletos honestos ("llega con E6") donde Sieve aún no aterrizó — P4.

### E6 — Sieve: filtros, vacaciones, reenvío, bloqueados [XL] — dep.: ninguna dura; UI de filtros tras E5 — **Fable 5**
- **C0 ManageSieve [M]:** cliente `dovecot:4190` con la app password (scope ya aprovisionado), encapsulado con la disciplina de `internal/imap`.
- **Vacaciones [M]:** `VacationResponse` RFC 8621 §8 → Sieve `vacation` con la spec exacta de Gmail: `:days 4`, sin respuesta a listas/spam, solo-contactos opcional, banner "Finalizar ahora", 12:00 AM / 11:59 PM.
- **Filtros [L-XL]:** `SieveScript` RFC 9661 (get/set/validate + blob) con particionado por origen (jamás destruir scripts ajenos — invariante "Dovecot es la fuente de verdad"); builder visual = álgebra GC-4; recetas de primer nivel: **bloquear remitente**, **reenvío** (con flujo de verificación GC-4), nunca-spam.
- **Cuota [S]:** `Quota/get` RFC 9425 + barra en Cuenta (Mailcow ya la expone).

### E7 — Identidad y envío [M] — dep.: E0
Firmas múltiples con nombre + defaults nueva-vs-respuesta (extensión del modelo Identity actual — gap real: la API de Gmail es plana pero su UI no, canon §3); Send & Archive; reenviar como adjunto `.eml`; modo texto plano (fila UNSOURCED en Gmail, registrada — comportamiento real); comportamiento de respuesta por defecto; **índice de direcciones auto-alimentado por el correo enviado** (el modelo "Other contacts" de Gmail — autocompletado sin subsistema de contactos, con opt-out "yo agrego mis contactos"); **extensiones ejecutables bloqueadas al adjuntar — bloqueo duro con la lista publicada de Gmail y su mensaje de error honesto** (es una postura de seguridad de Gmail: se sigue, no se diverge — P1).

### E8 — Labels/keywords bajo el techo [M] — dep.: E0, escape JSON-Pointer antes (mecanismo G3)
UI de keywords con GC-5: paleta cerrada, chips (jamás tinte de fila), `labelShowIfUnread` por mailbox, anidado por carpetas, techo 26 explicado en UI, rename como `migrateKeyword` acotado.

### E9 — PWA, offline y notificaciones [L] — dep.: E0 (settings de offline)
- **Instalable [S]:** manifest + iconos + SW mínimo + handler `mailto:` (+ `protocol_handlers` del manifest — algo que Gmail no puede tener). D-1.
- **Notificaciones desktop [S]:** los 3 modos exactos de Gmail sobre el SSE existente (GC-2) + indicador de desconexión (hoy la muerte del SSE es silenciosa — canon §6.10).
- **Offline real [L]:** SW + IndexedDB + **carpeta Outbox** (la forma Gmail: leer, buscar, responder offline; adjuntos no previsualizables se declara), setting de profundidad de sync, `recycleStaleSSE` en `visibilitychange`. D-2.

### E10 — Confianza [S-M] — dep.: ninguna — **Fable 5**
D-4 ejecutado: display por defecto vía proxy HMAC + supresión en sospechosos (verdicto Rspamd surfaceado) + setting always/ask; banner de spam que degrada affordances sin esconder el mail; tests-invariante de GC-6 (MDN) y GC-7 (URLs intactas); verificación de que ningún camino de render evita el chokepoint de sanitización.

### E11 — Teclado completo, pulido y cierre [M-L] — dep.: todas
Mapa Gmail completo sobre nuestro resolver puro (fix `event.code` — hoy los layouts no-latinos pierden todo), a11y (reduced-motion, focus-visible, focus-trap testeado), claves i18n nuevas con paridad forzada, **suite e2e Playwright de la capa de interacción** (donde Bulwark tiene cero — terreno de diferenciación), y el **gate final**: cada fila CORE del canon §2 y cada criterio del ADR §6 con su número, contra Gmail Y Bulwark, sin excepciones.

**Dependencias duras (todo lo demás paraleliza):** E0 → {E4, E5, E7, E8, E9-settings} · E1-servidor → E1-UI · E5 → UI de filtros(E6) · E11 al final. *Blandas declaradas:* sugerencias de direcciones en E3 esperan el índice de E7; SearchSnippet no depende de E1.

**Total estimado: 7-9 semanas incluyendo los 11 gates P2** (presupuestados a ½-1 día cada uno — el gate no es gratis y serializa en el borde de cada épica). Calibración P3: la suma de tallas da ~14-15 semanas secuenciales — consistente con la v2 — pero el antecedente real (motor+servidor+escrituras+PWA en ~3 semanas, 2026-08-07→08-26) demuestra el paralelismo ~2× sostenido de esta jerarquía; la ruta crítica es E0→E5→E6-UI (~5-6 semanas en el mejor caso) y E11 cierra detrás de todo. **E6 es el candidato a estallar** (Sieve + builder + flujo de verificación de reenvío, y es la épica donde una sorpresa de Dovecot no se absorbe con UI): por eso su estructura oficial es **pre-partida** — C0+vacaciones+recetas (bloquear/reenviar/nunca-spam) shippean primero; el builder completo es la cola, no el bloqueante. Se re-estima al abrir cada épica; el dueño firma sabiendo la magnitud y sabiendo que la v2 decía 12-15 sumando filas.

## 5. Cobertura del canon — verificación de completitud

Todo CORE del canon §2 tiene épica: conversación (E1) · archivo/spam/star/read/move/undo/hover (E2; **bloquear remitente en E6** — receta Sieve, referencia cruzada explícita) · papelera con vaciado + retención 30 días documentada (E2) · unsubscribe con List-Unsubscribe/RFC 8058 (E2) · snooze/mute/schedule (E4, diseño GC-10) · undo-send valores (E5) · firmas y adjuntos y autocomplete (E7) · inbox types determinísticos + reading pane + densidad (E5) · búsqueda (E3) · labels (E8) · teclado (E11) · vacation (E6) · notificaciones 2-modos (E9, GC-2) · offline (E9) · show-original (E2) · storage (E6-cuota). Los SEC entran donde son baratos (Send & Archive, forward-as-attachment, `_`, `[`/`]`) o se difieren con nombre (§6: superstars, Manage subscriptions, Multiple Inboxes, operadores de búsqueda de cola).

## 6. Diferido con nombre — decisión del dueño, no deriva

| Ítem | Talla | Por qué se difiere | Riesgo |
|---|---|---|---|
| Web Push (VAPID propio) | L | Más-allá-de-Gmail (GC-2: la paridad es foreground) | Bajo; diferenciación futura |
| Importance/Priority Inbox/categorías/Smart-* | XL | AI-gated en Gmail; fase IA tras consentimiento (GC-8, orden de Diego) | Ninguno para paridad — Gmail lo confirma |
| Superstars (12 tipos) | S-M | SEC; la estrella básica es CORE y entra | Se nota solo en power users |
| Manage subscriptions (vista por remitente + volumen) | M | SEC; unsubscribe por mensaje (E2) cubre el flujo diario | Bajo |
| Operadores de búsqueda de cola: `deliveredto: AROUND +word {} list: filename: rfc822msgid: header:` | S-M | Larga cola del lenguaje; el set E3 cubre el uso diario | Bajo; se añaden a demanda |
| Multiple Inboxes / Priority sections | M | SEC, "computer only" hasta en Gmail | Bajo |
| Plantillas + acción de filtro "Send template" | S | SEC web-only en Gmail; barata sobre E0 — candidata si sobra capacidad | Bajo |
| Delegación / buzones compartidos | XL | Dovecot ACL + semántica SMTP propia; límites de Gmail contradictorios entre sus propias fuentes | Visible en Workspace-parity, no en el piloto |
| Multi-cuenta en una sesión | XL | Orden de producto fijado; en Gmail web es switcher, no unificado | Conocido |
| Subsistema de contactos completo | XL | El modelo "Other contacts" (E7) cubre la paridad de autocompletado — bendecido por Gmail | Bajo con E7 |
| S/MIME | L | Estándar portable, fuera de MVP | Bajo |
| Traducir mensaje / confidential mode / AMP | — | N/A-Google (canon §3) | Ninguno |
| Cambio de contraseña / TOTP (vía API Mailcow) | M-L | Sin análogo Dovecot; panel Cuenta lo declara honesto | El hueco visible sigue; **decisión explícita de Diego** (heredada de v2) |

## 7. Riesgos

1. **Sieve (E6) es el ancla XL** y sostiene filtros+vacaciones+bloqueados+reenvío. Mitigación: C0 como fundación única testeada, particionado por origen, y su UI puede shippear por recetas (bloquear/reenviar) antes que el builder completo.
2. **El techo de 26 keywords** contra el modelo de labels: GC-5 lo diseña alrededor; el riesgo es de expectativa del usuario, se mitiga en la UI (honestidad) y con carpetas.
3. **D-7 (techo 10.000 vs 26.869 vs 100k)** — sigue siendo la decisión con números pendiente; E3 no cierra sin resolverla o firmarla.
4. **Borrar carpeta 1,8-6,1 s** (deuda W4b) viola la vara; es scope explícito de E1-servidor (interacción DELETE/reconciler) y si el fix IMAP no alcanza, UX honesta.
5. **La superficie crece más rápido que la calidad** — P2 es la mitigación: ninguna épica cierra sin su gate doble.
6. **Drift del esquema de preferencias** — versionado con cadena de migración desde E0 (heredado v2, vigente).
7. **Salud del oráculo Bulwark** — capability vendor ignorada verificada en CI; el Bulwark del piloto (v1.8.1) sigue con advisory sin parchear: actualizarlo es tarea de operación del piloto, fuera del plan pero a la vista del dueño.
8. **El canon envejece** — Gmail cambia; cada gate P2 re-verifica contra la página viva las filas que toca, y el canon registra fecha de recuperación.

## 8. Verificación adversarial (ejecutada 2026-08-30)

- [x] Agente independiente (Fable 5) auditó: cobertura del canon, afirmaciones de código contra el repo, consistencia interna, estimación, mecánica de gates. **16 hallazgos** (7 obligatorios). Los obligatorios y los baratos están incorporados en esta versión: unsubscribe entró a E2 con RFC 8058; el diseño de persistencia de snooze/mute se fijó en GC-10 (la versión anterior violaba "Dovecot es la fuente de verdad"); las notificaciones bajaron a 2 modos pre-IA (el modo "importante" sin clasificador era un control muerto); los prerrequisitos operativos del gate P2 se nombraron (la investigación anterior falló exactamente ahí); las extensiones bloqueadas pasaron de "aviso" a bloqueo duro (era una divergencia de seguridad sin arbitraje); TNEF obtuvo épica dueña (E2) y referencia corregida; la retención de papelera obtuvo línea con dueño; `bcc` corregido (JSONB, no columna); el spam del lector reclasificado como new-build; dependencias reconciliadas; la estimación se re-expresó como 7-9 semanas incluyendo gates, con E6 pre-partido como estructura oficial. De ejecución quedaron los menores 12-14 (registrados arriba donde tocan).
- [x] Auditoría verificada además contra el código por el director en los dos hallazgos de mayor carga (`collapseThreads` obsoleto; props del lector).

## 9. Aprobación

- [x] Verificación adversarial (§8) completada e incorporada (2026-08-30)
- [ ] Firma de Diego: el proyecto completo, las 8 decisiones D-1…D-8, y los diferidos §6
- [ ] Solo entonces: ejecución (equipos por épica, gates P2 por entrega, sin deploy ni push sin autorización explícita)
