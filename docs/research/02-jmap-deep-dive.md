# JMAP Deep Dive — ¿Exponer JMAP estándar o API propia?

> Research documental para el proyecto "webmail open source para Mailcow"
> Fecha: 2026-08-07 · Autor: research agent (Grupo NU)
> Alcance: 100% documental (web + GitHub). No se tocó ningún servidor.

---

## 0. TL;DR de la recomendación

**Recomendación: JMAP estándar como superficie pública del sync engine, implementado por fases (subset RFC 8620 + RFC 8621 core primero), NO una API propietaria.**

El argumento decisivo no es ideológico sino de economía de esfuerzo: el trabajo duro de la "Opción B" (sync engine IMAP → base propia, modelo de datos normalizado, tokens de estado incrementales, threading) **hay que hacerlo igual**, exponga uno JMAP o una API propia. Ese es ~80% del costo. Lo que cambia es la capa fina de serialización HTTP: si esa capa habla JMAP, se hereda gratis un ecosistema de clientes móviles y librerías; si habla un dialecto propio, hay que escribir y mantener cada cliente uno mismo, para siempre.

Además, y esto es lo que más pesa: **existe un precedente vivo y validado de exactamente esta arquitectura** — `jmap-perl` (el proxy de Fastmail) hace hoy sync IMAP→SQLite y expone JMAP RFC-compliant, con 132/132 tests del JMAP TestSuite pasando, actualizado a mayo de 2026. No estaríamos inventando el mapeo; estaríamos re-implementando uno documentado y demostrado.

Riesgo principal a gestionar: JMAP tiene una **cola larga de superficie** (blobs, EmailSubmission, SearchSnippet, push, Sieve) donde el "subset" se vuelve resbaladizo, y los clientes de terceros asumen partes de esa cola. Ver §6.

---

## 1. El protocolo: resumen operativo

### 1.1 Arquitectura general (RFC 8620 — JMAP Core)

JMAP es un protocolo JSON sobre HTTP, orientado a **objetos + estado**, no a comandos. Sus piezas:

**Session object.** El cliente hace `GET /.well-known/jmap` (autenticado) y recibe un documento que declara: capabilities soportadas, lista de accounts accesibles, `apiUrl`, `downloadUrl`, `uploadUrl`, `eventSourceUrl`, y límites del servidor (`maxSizeUpload`, `maxCallsInRequest`, `maxObjectsInGet`, etc.). Es el punto de descubrimiento — un cliente bien escrito no hardcodea nada más que la URL de sesión.

**Request/Response batching.** Toda la API vive en un único endpoint POST. El request es:

```json
{
  "using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
  "methodCalls": [
    ["Email/query",  { "accountId": "u1", "filter": {...} }, "c0"],
    ["Email/get",    { "accountId": "u1", "#ids": {
        "resultOf": "c0", "name": "Email/query", "path": "/ids"
    }}, "c1"]
  ]
}
```

Dos mecanismos clave acá:
- **Batching**: N llamadas en un round-trip.
- **Back-references** (`#ids` con `resultOf`/`path`): el resultado de una llamada alimenta a la siguiente **server-side**, sin round-trip intermedio. Esto es lo que hace que "dame los 50 mails de la bandeja con sus headers" sea 1 request en vez de 2 o 51. Es la ventaja de performance real de JMAP sobre IMAP/REST ingenuo.

**El patrón de métodos uniforme.** Cada tipo de objeto expone un subconjunto canónico:

| Método | Qué hace |
|---|---|
| `Foo/get` | Trae objetos por id (con `properties` para proyección parcial) |
| `Foo/changes` | Dado un `sinceState`, devuelve `created`/`updated`/`destroyed` |
| `Foo/set` | Crea/actualiza/destruye en batch; devuelve `created`/`updated`/`notCreated`... |
| `Foo/query` | Devuelve lista de ids según `filter` + `sort` (+ paginación) |
| `Foo/queryChanges` | Delta de una query previamente ejecutada |
| `Foo/copy` | Copia entre accounts |

Esta uniformidad es la mejor propiedad del diseño: un cliente que entiende el patrón para `Email` lo entiende para `Mailbox`, `Contact`, `CalendarEvent`. También significa que implementar el patrón bien una vez, en nuestro sync engine, cubre todos los tipos.

**Sync vía state strings.** Cada tipo tiene un `state` opaco por account. El cliente guarda el state; cuando quiere actualizarse llama `Foo/changes` con `sinceState`. El servidor responde con los deltas y un `newState`. Si el servidor no puede calcular el delta (state demasiado viejo, purga de log), devuelve el error `cannotCalculateChanges` y el cliente hace resync completo. **Este es el contrato central de JMAP y el que más condiciona nuestro diseño de base**: hay que poder responder "¿qué cambió desde X?" de forma barata, lo que implica un changelog persistente con retención, no solo el estado actual.

**Blobs.** Los binarios (adjuntos, mensajes crudos) no viajan en JSON. Se suben a `uploadUrl` (devuelve `blobId`) y se bajan de `downloadUrl` (template con `{blobId}`, `{type}`, `{name}`). RFC 9404 (JMAP Blob Management) agrega `Blob/upload`, `Blob/get` y `Blob/lookup` para manipularlos dentro del batch JSON. Un `blobId` es la unidad de contenido inmutable — y en un backend IMAP, mapear blobIds estables es uno de los puntos finos (§7.5).

**Push.** Dos transportes, ambos en RFC 8620:
- **EventSource** (SSE): `GET` al `eventSourceUrl`, streaming unidireccional sobre HTTP plano. Atraviesa proxies corporativos. Es el que usan los webmails.
- **PushSubscription**: el cliente registra una URL externa (webpush) para push a móviles cuando la app está cerrada. RFC 9749 agrega VAPID para autenticar el push.

El payload del push es un **StateChange**: no manda el contenido, manda "el state de Email en la cuenta u1 ahora es Y". El cliente entonces llama `Email/changes`. Diseño correcto: el push es un *hint*, la verdad está en `/changes`. Eso significa que si el push falla, el cliente degrada a polling sin romperse.

**RFC 8887 (JMAP over WebSocket).** Binding alternativo: subprotocolo WebSocket que transporta los mismos requests/responses más los push, evitando el overhead de HTTP por llamada y la conexión SSE separada. Implementado por Apache James y por la librería Rust de Stalwart.

### 1.2 RFC 8621 — JMAP Mail: el modelo de datos

**Mailbox.** Carpeta/label. Campos: `id`, `name`, `parentId` (jerarquía acíclica), `role` (`inbox`, `sent`, `drafts`, `trash`, `junk`, `archive` — semántica tipo SPECIAL-USE), `sortOrder`, `totalEmails`, `unreadEmails`, `totalThreads`, `unreadThreads`, y `myRights` (ACL). Métodos: get/changes/query/queryChanges/set.

> ⚠️ Los contadores `unreadThreads` son sorprendentemente caros de mantener correctamente en un backend IMAP y son un foco de bugs. Requieren mantener el join mailbox×thread materializado.

**Thread.** Objeto minimalista: `id` + `emailIds` (ordenados por `receivedAt`, más viejo primero). Solo get/changes — no se puede crear ni modificar un Thread; es derivado. La pertenencia se calcula server-side.

**Email.** El objeto central, y el más gordo. Expone el mensaje en varias capas simultáneas:
- Metadata: `id`, `blobId`, `threadId`, `mailboxIds`, `keywords`, `size`, `receivedAt`.
- Headers: acceso crudo (`headers`), por nombre y forma tipada (`header:Subject:asText`, `header:To:asAddresses`, `header:List-Id:asURLs`, etc.) y propiedades convenientes (`subject`, `from`, `to`, `sentAt`, `messageId`, `inReplyTo`, `references`).
- Cuerpo: `bodyStructure` (árbol de EmailBodyPart), `textBody`/`htmlBody` (listas ya "aplanadas" para render), `attachments`, `preview`, `bodyValues` (contenido inline, con truncado configurable).

Métodos: get/changes/query/queryChanges/set/copy/**import**/**parse**.

Dos representaciones críticas, ambas como mapas a boolean:

```json
"mailboxIds": { "mbx-inbox": true, "mbx-work": true },
"keywords":   { "$seen": true, "$flagged": true, "proyecto-x": true }
```

Un Email **debe** pertenecer a ≥1 Mailbox siempre. Mover = un `Email/set` que reescribe `mailboxIds`. Notar que el `id` **no cambia al mover** — divergencia fundamental con IMAP, donde mover es COPY+EXPUNGE y genera UID nuevo (§7.1).

Los keywords son los flags de IMAP más tags arbitrarios: `$seen`, `$flagged`, `$draft`, `$answered`, `$forwarded` mapean a flags IMAP estándar; el resto son keywords IMAP arbitrarios. RFC 8621 dice explícitamente que "keywords are shared with IMAP".

**Threading (definición normativa).** Dos mensajes van al mismo Thread si: (a) comparten un message-id entre `Message-ID`/`In-Reply-To`/`References`, **y** (b) tras normalizar el subject (quitando prefijos tipo `Re:`/`Fwd:` y espacios), los subjects coinciden. La cláusula (b) evita que un hilo se contamine por un reply reciclado.

> ⚠️ Punto duro: si por entrega desordenada dos threads deben fusionarse, el servidor **debe** hacerlo borrando y reinsertando los Emails que cambian de `threadId`, **con id nuevo**. O sea: el `threadId` de un Email es efectivamente inmutable, y el precio de fusionar hilos es churn de ids visible al cliente. Hay que diseñar el changelog para tolerar esto.

**EmailSubmission.** Modela el envío: referencia un `emailId` + `identityId`, más `envelope` (MAIL FROM / RCPT TO), `sendAt` (delayed send), y `undoStatus`. El patrón idiomático es un solo `Email/set` (crea el draft) + `EmailSubmission/set` (lo envía) + `onSuccessUpdateEmail` (mueve a Sent y quita `$draft`) — todo en un request.

**Identity.** Las direcciones "from" disponibles del usuario. **VacationResponse.** Autorespuesta (singleton, id `"singleton"`). **SearchSnippet.** Fragmentos con highlight para resultados de búsqueda — solo `get`.

### 1.3 Extensiones: estado a agosto 2026

| Spec | RFC / Draft | Estado |
|---|---|---|
| JMAP Core | **RFC 8620** | Publicado (2019) |
| JMAP Mail | **RFC 8621** | Publicado (2019) |
| JMAP over WebSocket | **RFC 8887** | Publicado |
| JMAP MDN | **RFC 9007** | Publicado |
| JMAP S/MIME verification | **RFC 9219** | Publicado |
| JMAP Blob Management | **RFC 9404** | Publicado |
| **JMAP Quotas** | **RFC 9425** | Publicado |
| **JMAP Sieve Scripts** | **RFC 9661** | Publicado (sept 2024) |
| JMAP Sharing | **RFC 9670** | Publicado |
| **JMAP Contacts** | **RFC 9610** | Publicado |
| JSContact (formato) | RFC 9553 | Publicado |
| **JMAP Calendars** | **draft-ietf-jmap-calendars-27** | ⚠️ **Todavía draft** (jul 2026, expira ene 2027) |
| JSCalendar 2.0 | draft | En progreso |
| JMAP Tasks | draft | En WG, milestone IESG ~jul 2026 |
| JMAP Email Push (delivery) | draft-ietf-jmap-emailpush | Draft (implementado por Stalwart) |

**Lectura para nosotros:** Mail, Contacts, Sieve y Quotas son RFC estables. **Calendars sigue siendo draft después de 27 revisiones** — dato relevante si alguna vez quisiéramos reemplazar CalDAV de SOGo: ese terreno todavía se mueve. Para un webmail de correo puro, el terreno normativo está firme.

**Nota sobre Sieve (RFC 9661):** es directamente relevante para Mailcow, que ya expone Sieve vía ManageSieve en Dovecot. Exponer `SieveScript/*` sobre nuestro backend sería un mapeo casi 1:1 contra ManageSieve, y daría gestión de filtros en el webmail con un estándar en vez de un endpoint inventado.

---

## 2. Implementaciones servidor

### 2.1 Stalwart (Rust) — la referencia de facto

Servidor all-in-one (IMAP, JMAP, SMTP, CalDAV, CardDAV, WebDAV), AGPLv3. Es hoy **la implementación JMAP más completa que existe**: fue el primer servidor en soportar la familia completa de protocolos de colaboración JMAP (Calendars, Contacts, File Storage, Sharing), incluyendo drafts como `emailpush`. Se declaró feature-complete y trabaja hacia el 1.0.

Relevancia para nosotros: **es el benchmark de comportamiento, y es competencia conceptual**. Si algún día Mailcow fuera reemplazable por Stalwart, el webmail JMAP que escribamos seguiría funcionando sin cambios — eso es exactamente el argumento a favor del estándar. Stalwart además planea su propio webmail (SPA en Rust/Dioxus) para 2026, y ya existen webmails de terceros para él (Bulwark, Kolumba).

> Insight estratégico: si exponemos JMAP, nuestro webmail **también funcionaría contra Stalwart**. Eso convierte el proyecto de "webmail para nuestro Mailcow" en "webmail para cualquier backend JMAP", multiplicando su valor y su posible comunidad. Con API propia, queda atado a nuestro sync engine para siempre.

### 2.2 Cyrus IMAP (C) — JMAP en producción real

Cyrus tiene soporte JMAP mantenido por ingenieros de Fastmail, que lo usan en su servicio comercial. La documentación oficial lo describe como *work in progress* incluso en la rama 3.12.x. Limitaciones documentadas: no implementa autenticación JMAP propia (requiere HTTP Basic Auth por request), blobs/attachments incompletos en algunas versiones, y no soporta creación de mensajes en múltiples mailboxes ni mailbox moves.

**Dato valioso:** Cyrus es el backend contra el cual `jmap-perl` corre su suite de 132 tests. O sea, es el "servidor de referencia" práctico para validar conformidad.

### 2.3 Apache James / tmail-backend (Java)

James implementó primero un `jmap-draft` (spec vieja, pre-RFC) y después una implementación RFC-8621 en puerto separado, seleccionable vía `Accept: ...;jmapVersion=rfc-8621`. La documentación del propio proyecto **admite explícitamente los límites**: métodos y tipos no implementados, implementaciones "naive" y — en la documentación consultada — PUSH no soportado en la implementación RFC (aunque hay ADR de push sobre WebSocket).

`tmail-backend` (Linagora) extiende James y es el backend de Twake Mail.

**Este es el precedente más importante para la pregunta "¿se puede JMAP parcial?":** James lleva años en producción con un RFC 8621 **admitidamente incompleto**, y clientes reales (Twake Mail) funcionan contra él. Prueba de que el subset es viable en la práctica.

### 2.4 Proxies / gateways JMAP↔IMAP — el hallazgo central

Acá está lo más relevante del research, y hay que separar dos direcciones que suelen confundirse:

**Dirección A — JMAP-sobre-IMAP (lo que necesitamos): `jmap-perl`.**

El viejo "jmap-proxy" de Fastmail **no está muerto: está vivo y modernizado.**

| Dato | Valor |
|---|---|
| Repo | `github.com/jmapio/jmap-perl` |
| Último push | **2026-05-18** |
| Actualizado | 2026-07-20 |
| Archivado | No |
| Commits | ~920 |
| Stars | 173 |
| Licencia | MIT |
| Demo vivo | `proxy.jmap.io` |

Lo que dice su propia documentación:
- "Syncs email from any IMAP server and exposes it over JMAP (RFC 8621)"
- **132/132 tests del JMAP TestSuite pasando** contra Cyrus IMAP, cubriendo RFC 8620 core, RFC 8621 mail, JMAP Calendars y JMAP Contacts (RFC 9610)
- Backends IMAP + CalDAV + CardDAV, con conversión a JSCalendar/JSContact
- Passthrough directo para backends que ya hablan JMAP
- Multi-tenant, OAuth2 (Gmail, Fastmail), cifrado de credenciales (AES-256-GCM u OpenBao Transit)
- Docker, métricas Prometheus, health checks
- Corriendo en producción en `proxy.jmap.io` con webmails Bulwark y TMail encima

**Su arquitectura es, esencialmente, nuestra "Opción B":**
- Modelo fork padre/hijo: el padre nunca bloquea, rutea HTTP; un worker por cuenta
- **Una SQLite por cuenta** en `/data`
- Flujo: `setuser()` (credenciales IMAP) → `firstsync()` (sync inicial) → `sync_imap()` (incremental)
- Capa `JMAP::ImapDB` que hace el mapeo UID↔JMAP id, threading y state tokens
- Push vía Server-Sent Events

Gaps declarados en su ROADMAP: free/busy de calendario, modelo Principal completo, `CalendarEventNotification`, métodos `PushSubscription` (usa SSE en su lugar), **sin soporte de email en múltiples mailboxes**, sin modelo de sharing/delegación (decisión de diseño), máximo 1 calendario por evento.

> **Conclusión operativa:** existe una implementación de referencia, MIT, viva, testeada contra la suite oficial, que hace exactamente lo que queremos hacer. Aunque no usemos Perl, **su modelo de datos, su estrategia de mapeo y su lista de gaps aceptables son un plano de ingeniería listo para leer.** Reduce muchísimo el riesgo técnico de "JMAP sobre IMAP es demasiado difícil". Y su lista de gaps nos dice cuáles son los recortes que un proxy real se permite.

**`jmap-proxy-python` (filiphanes): muerto.** Último push **2021-08-16**, 24 stars, README dice "WARNING: in development". Interesante solo por un dato: documenta las capabilities IMAP que exige — `ENABLE, SPECIAL-USE, CONDSTORE, ESEARCH, ESORT, QRESYNC, UTF8=ACCEPT, METADATA` — y declara que funciona contra Dovecot. Eso es una lista de requisitos validada para nuestro sync engine contra Dovecot.

**`mailjail`** (Python, en awesome-jmap): proxy JMAP-IMAP con acceso restringido. Nicho.

**Dirección B — IMAP-sobre-JMAP (lo inverso, NO nos sirve): `stalwartlabs/imap-to-jmap`.** **Archivado desde 2024-10-08.** Es un servidor IMAP que habla con un backend JMAP (para que clientes IMAP legacy usen un servidor JMAP). Desarrollo movido dentro del repo principal de Stalwart. Cuidado con el nombre: es la dirección contraria a la que necesitamos.

### 2.5 Dovecot / Mailcow: ¿JMAP nativo? — No, y no viene

Este punto es crítico porque define que **no hay atajo**:

- JMAP se anunció para Dovecot v2.3 (~2016) y **nunca se materializó**.
- Dovecot llegó a escribir una librería JSON nueva y código inicial para reusar el proceso IMAP, y planeó una "arquitectura de servidor HTTP" genérica que habilitara JMAP — **ese trabajo no se completó**.
- La posición del equipo Dovecot en su lista de correo: **no tienen planes de trabajar en JMAP**, aunque no impiden contribuciones externas.
- Dovecot 2.4 (2026) trae features experimentales (`--enable-experimental-imap4rev2`, mail UTF-8) y una tanda de parches de seguridad — **nada de JMAP**.
- El issue de mailcow pidiendo JMAP (mailcow-dockerized#3133, abierto en nov-2019) está **cerrado** sin implementación.

> **Conclusión:** si queremos JMAP contra Mailcow, **lo tenemos que construir nosotros**. No hay ruta en la que Dovecot nos lo entregue. Esto no es un argumento contra JMAP — es la confirmación de que la "Opción B" (sync engine intermedio) es la única arquitectura posible, independientemente de qué API expongamos al final.

---

## 3. Clientes JMAP existentes: ¿qué heredaríamos gratis?

Esta es la pregunta que más pesa en la decisión, así que hay que ser honestos sobre la diferencia entre "lista larga" y "ecosistema real".

### 3.1 Móviles

| Cliente | Plataforma | Tech | Estado real |
|---|---|---|---|
| **Twake Mail** | Android, iOS, web | Dart/Flutter | ✅ **Activo** — v0.28.x actualizada en 2026. Linagora. El más vivo. |
| **Ltt.rs** | Android | Java | ⚠️ **Estancado** — última release 0.4.3 (ene-2024). PoC de Daniel Gultsch. |
| Sterna Mail | Android | Kotlin | GPLv3, nicho |
| Mailtemi | iOS/Android/desktop | — | Comercial |
| Plume | iOS/macOS | — | Comercial, privacy-focused |
| Aria | Multiplataforma | Kotlin Multiplatform | Nicho |
| Leithmail | web/Android/iOS | Dart/Flutter | Nicho |

**Veredicto móvil, sin edulcorar:** el ecosistema es **real pero delgado**. Hay esencialmente **un** cliente móvil libre, maduro y activamente mantenido (Twake Mail). Ltt.rs, que era el poster child, lleva ~2 años sin release. La promesa "exponemos JMAP y nuestros usuarios tienen apps móviles nativas gratis" es **verdadera pero frágil**: descansa sobre un solo proyecto.

Contraargumento importante que igual sostiene la decisión: la alternativa no es mejor. Con API propia, el número de clientes móviles de terceros es **exactamente cero, garantizado y para siempre**. Y los usuarios de Mailcow ya tienen apps móviles vía IMAP/ActiveSync — que seguirán funcionando en paralelo, porque el sync engine no reemplaza a Dovecot, se apoya en él. O sea: JMAP en móvil es **upside opcional**, no un requisito del proyecto.

### 3.2 Escritorio / terminal

**aerc** (Go, terminal — con soporte JMAP), **meli** (Rust, GPLv3), **Parula**, **ratatoskr** (Rust, multi-protocolo), **mujmap** (sync con notmuch), **Vandelay** (migración/backup), **mjmap** (cliente sendmail-compatible), **JMAP Screener** (filtrado).

Nicho pero técnicamente valioso: herramientas como Vandelay (backup/migración) y mujmap son *exactamente* el tipo de utilidad operativa que uno querría tener gratis en una infra de mail administrada. Poder hacer backup/migración de una cuenta con una herramienta estándar tiene valor concreto para Grupo NU.

### 3.3 Webmails JMAP (referencia de diseño, y competencia)

**Bulwark** (Next.js, AGPLv3 — webmail para Stalwart), **Kolumba** (para Stalwart), **JMAP Webmail** (TypeScript, MIT, con push), **JMAP Demo Webmail** (JS, MIT, de Fastmail), **Cypht** (PHP, agregador), **Group-Office**, **roundcube-jmap** (plugin JMAP para Roundcube).

> **Dato que cambia el análisis:** si exponemos JMAP, **Bulwark y TMail ya corren hoy sobre `jmap-perl`** — es decir, sobre un backend IMAP proxied. Hay evidencia empírica de que webmails JMAP de terceros funcionan contra un sync engine IMAP→JMAP. Eso nos daría un **fallback**: si nuestro frontend PWA se atrasa, hay webmails usables desde el día uno. Y un **oráculo de testing**: podemos validar nuestro servidor contra clientes que no escribimos nosotros — el mejor detector de "implementamos el estándar mal".

### 3.4 Librerías cliente (lo que usaría nuestro propio frontend)

| Librería | Lenguaje | Notas |
|---|---|---|
| **jmap-jam** | TS | ~2kb gzip, zero-deps, tipada, fetch/ESM (Node ≥18 + browser). Lo más limpio para un PWA. |
| **jmap-kit** | TS | SDK type-safe: session discovery, invocation factories tipadas, dispatch de respuestas, plugins |
| **jmap-client-ts** | TS | Linagora. RFC 8620/8621, transport pluggable (fetch/axios/XHR) |
| jmap-yacl | TS | Ligera |
| JMAP-JS | JS | Fastmail; modelo mail+calendar+contacts (el que usa su webmail) |
| ⚠️ `linagora/jmap-client` | JS | **Basada en un draft viejo, pre-RFC. No usar.** |
| jmap-client (Stalwart) | Rust | Core/Mail/WebSocket completo, incl. SSE y WS async |
| go-jmap, jmapc (Python), JMAP.Net, swift-jmap-client | varios | Ecosistema de servidor/tooling |

**Veredicto librerías:** acá el ecosistema sí es **sano de verdad**. Para un frontend PWA en TypeScript hay al menos tres librerías modernas y mantenidas. Esto tiene un efecto directo y medible: **nos ahorra escribir y mantener el SDK cliente de nuestra propia API**, que es trabajo real y recurrente que suele subestimarse (tipos, reintentos, paginación, manejo de estado, invalidación de caché).

También existe `jmap-mcp` (servidor MCP sobre jmap-jam) — anecdótico, pero muestra que la superficie estándar atrae integraciones que uno no planeó. Con API propia, esas integraciones no ocurren nunca.

---

## 4. El trade-off: JMAP completo vs subset vs API propia

### 4.1 ¿Cuánto cuesta JMAP realmente?

Hay que descomponer el costo, porque el error típico es cargarle a "JMAP" costos que en realidad son de la Opción B:

**Costo que existe igual, exponga lo que exponga (≈75-80% del esfuerzo):**
- Sync engine IMAP (CONDSTORE/QRESYNC, IDLE, reconexión, backpressure)
- Esquema de base: mensajes, mailboxes, flags, threads, blobs
- **Changelog/versionado para deltas incrementales** (necesario para cualquier API moderna con sync)
- Threading server-side
- Parsing MIME, extracción de cuerpos, previews, adjuntos
- Índice de búsqueda
- Envío (SMTP submission) y drafts
- Autenticación, multi-cuenta, seguridad

**Costo incremental atribuible a JMAP (≈20-25%):**
- Session object + descubrimiento (chico)
- Serialización del modelo Email de RFC 8621 con sus formas de header (`asText`/`asAddresses`/`asURLs`), `bodyStructure`, `bodyValues` (**mediano-grande: la superficie de propiedades del Email es la parte más pesada de la spec**)
- Batching + resolución de back-references (mediano; es un mini-intérprete de JSON pointer sobre resultados previos)
- Semántica exacta de `/set` (patch parcial con paths tipo `keywords/$seen`, `notCreated`/`notUpdated` con SetError tipados)
- Semántica de `/query` + `/queryChanges` (**el más subestimado**: `queryChanges` con `upToId` y posiciones es genuinamente difícil de hacer bien)
- Endpoints de upload/download + blobId estables
- Manejo de errores canónico y `state` consistente

El costo incremental **no es despreciable** — pero es de naturaleza distinta al trabajo del sync engine: es **especificación conocida y cerrada**, con tests de conformidad disponibles y una implementación de referencia MIT para consultar. Es trabajo acotado y verificable. El costo de una API propia, en cambio, es **diseño abierto**: hay que inventar el modelo de deltas, la paginación, el manejo de errores, la versión de la API — decisiones que se toman mal la primera vez y se pagan con migraciones.

### 4.2 ¿Qué subset mínimo hace funcionar clientes de terceros?

Combinando el draft `jmap-essential`, los gaps aceptados por `jmap-perl` y los límites reconocidos de Apache James, este es el corte defendible:

**Fase 1 — "un cliente JMAP real se conecta y lee mail":**
```
Session object (con capabilities core + mail, límites reales)
Core/echo
Mailbox/get, Mailbox/changes, Mailbox/query
Email/get, Email/changes, Email/query, Email/queryChanges
Thread/get, Thread/changes
Identity/get
downloadUrl / uploadUrl operativos (blobs)
Back-references + batching  ← NO opcional: los clientes reales los usan
```
Sin esto los clientes no arrancan. Notablemente, **batching y back-references no son un lujo**: `jmap-jam`, TMail y Bulwark los usan idiomáticamente. Un servidor que no los soporte "funciona" pero rompe a los clientes buenos.

**Fase 2 — "se puede usar como cliente de correo":**
```
Email/set (keywords, mailboxIds → leído/no leído, mover, borrar, flag)
Mailbox/set (crear/renombrar/borrar carpetas)
EmailSubmission/set + onSuccessUpdateEmail  (enviar)
Email/import  (opcional pero útil)
EventSource push (SSE)
```

**Fase 3 — paridad y confort:**
```
SearchSnippet/get (highlight de búsqueda)
VacationResponse/get+set  (autorespuesta)
SieveScript/* (RFC 9661)  ← mapea a ManageSieve de Dovecot, alto valor/bajo costo
Quota (RFC 9425)
PushSubscription (webpush móvil) + VAPID
WebSocket (RFC 8887)
Email/copy, Blob/* (RFC 9404), MDN (RFC 9007)
```

**Recortes explícitamente aceptables** (precedente: los toma `jmap-perl` en producción):
- Sin sharing/delegación (RFC 9670) — no lo necesitamos
- Sin multi-mailbox por Email: declarar `mayCreateTopLevelMailbox` y comportarse como "un Email en una Mailbox". Es lo que hace `jmap-perl` y lo que hace Cyrus. **Es también la semántica natural de IMAP**, así que no es un recorte: es honestidad con el backend.
- Sin Calendars/Contacts JMAP en v1 — SOGo ya cubre CalDAV/CardDAV en Mailcow, y Calendars sigue siendo draft.
- `Email/parse` puede diferirse.

**Sobre `jmap-essential`:** ⚠️ conviene registrar que es un **draft EXPIRADO** (draft-ietf-jmap-essential-01, ago-2024, expiró feb-2025), de audriga, y orientado a **portabilidad de datos** (export/import), no a webmail interactivo. Sus perfiles (Bare Minimum / Essential Export / Essential Import) excluyen `/changes`, batching y push — precisamente lo que un webmail necesita. **No es la guía para nuestro subset**, pero es útil como evidencia de que la comunidad IETF reconoce formalmente que JMAP parcial es legítimo.

### 4.3 Casos reales de JMAP parcial

- **Apache James**: RFC 8621 con métodos/tipos faltantes e implementaciones "naive" declaradas en su propia documentación — y aun así Twake Mail (cliente real, en tiendas) funciona contra él.
- **Cyrus**: sin auth JMAP propia, blobs incompletos, sin multi-mailbox ni mailbox moves — y es el backend JMAP de Fastmail en producción.
- **jmap-perl**: gaps documentados en push subscriptions, sharing, multi-mailbox — y corre webmails de terceros en `proxy.jmap.io`.

> **Conclusión fuerte y bien soportada: nadie implementa JMAP al 100%.** El estándar tolera bien la implementación parcial *por diseño* — el mecanismo `using`/capabilities está hecho exactamente para negociar qué soporta el servidor. "JMAP subset" no es una traición al estándar; es cómo el estándar se usa en el mundo real.

---

## 5. Mapeo JMAP ↔ IMAP: los problemas duros

Estos son los riesgos técnicos concretos del sync engine. **Aplican casi todos aunque expongamos API propia** — son problemas de sincronizar IMAP, no de hablar JMAP. Eso es central para el argumento final.

### 5.1 Identidad de mensajes: UID/UIDVALIDITY vs id estable

- IMAP: la identidad es `(mailbox, UIDVALIDITY, UID)`. Al mover un mensaje (COPY+EXPUNGE) **cambia el UID**. Si el servidor cambia `UIDVALIDITY`, todos los UIDs cacheados de esa carpeta son inválidos y hay que purgar y re-sincronizar.
- JMAP: el `id` del Email es estable y **no cambia al mover entre mailboxes**.

**Implicancia:** necesitamos una tabla de identidad propia con id sintético estable, más un mapa a `(folder, uidvalidity, uid)` que se actualiza en cada move. La clave natural para reconciliar tras un `UIDVALIDITY` roto es un hash de contenido y/o `Message-ID`. Es exactamente lo que hace la capa `ImapDB` de `jmap-perl`.

⚠️ Riesgo real: si el usuario mueve mail desde **otro** cliente IMAP (Thunderbird, el móvil), nosotros vemos EXPUNGE en A + APPEND en B. Hay que reconciliar eso como "move" y no como "borrar + crear un mail nuevo", o el usuario ve mails que desaparecen y reaparecen con hilos rotos y contadores mal. Requiere ventana de reconciliación por Message-ID/hash.

### 5.2 Detección de cambios: CONDSTORE / QRESYNC

- **CONDSTORE (RFC 7162)**: cada mensaje tiene `MODSEQ`; se pide "cambios desde MODSEQ X". Resuelve cambios de flags incrementales.
- **QRESYNC (RFC 7162)**: agrega la lista de **mensajes borrados** desde un modseq (`VANISHED`), que CONDSTORE solo no da eficientemente.

Juntos son la fuente natural del `Foo/changes` de JMAP: `HIGHESTMODSEQ` es casi literalmente un `state` token.

✅ **Buena noticia para Mailcow:** Dovecot soporta CONDSTORE y QRESYNC. `jmap-proxy-python` los lista como requisito y declara funcionar contra Dovecot. O sea, el sustrato existe.

⚠️ Pero: el `state` de JMAP es **por account**, mientras `HIGHESTMODSEQ` es **por mailbox**. Hay que sintetizar un state global — típicamente un contador monótono propio en nuestra base, incrementado por cada cambio aplicado. No se puede usar el modseq de IMAP directamente como state JMAP. Y hay que persistir un **changelog con retención** para poder responder `/changes`; cuando el `sinceState` es más viejo que la retención, devolver `cannotCalculateChanges` (que es comportamiento legítimo y previsto).

### 5.3 Threading

IMAP no da threads (salvo la extensión `THREAD`, cuya calidad varía y que no es incremental). JMAP los exige como objeto de primera clase.

Hay que implementar el algoritmo de RFC 8621 (message-ids + subject normalizado) sobre nuestra base, incrementalmente y de forma consistente:
- Mantener un índice `message-id → thread`
- Al llegar un mail, resolver referencias y unir componentes
- ⚠️ **El caso feo: la fusión de threads.** Con entrega desordenada, dos threads existentes pueden resultar ser uno. RFC 8621 obliga a **destruir y recrear con id nuevo** los Emails que cambian de `threadId`. Nuestro changelog debe emitir destroyed+created, y el frontend debe tolerarlo.
- Normalización de subject en español: hay que manejar `Re:`, `RE:`, `Fwd:`, `FW:`, **`RV:`, `Responder:`, `Reenviar:`** — los prefijos localizados son una fuente clásica de hilos partidos con clientes en español. Nos afecta directamente.

### 5.4 Keywords / flags

Mapeo mayormente directo: `$seen`/`$flagged`/`$draft`/`$answered` ↔ `\Seen`/`\Flagged`/`\Draft`/`\Answered`; keywords arbitrarios ↔ keywords IMAP.

⚠️ Detalles: `\Recent` no existe en JMAP (bien, es un flag inútil); `\Deleted` + EXPUNGE es el modelo IMAP de borrado, mientras JMAP borra moviendo a la mailbox con role `trash` o destruyendo el Email. Hay que decidir la política de destrucción (mover a Trash vs `\Deleted`+EXPUNGE) y ser consistente. Además, no todos los servidores IMAP permiten keywords arbitrarios en todas las carpetas (`PERMANENTFLAGS` con `\*`); Dovecot generalmente sí.

### 5.5 Blobs y blobIds estables

`blobId` debe ser estable e inmutable para el mismo contenido. En IMAP el contenido se referencia por `(mailbox, uid, part)` vía FETCH BODY[...] — que cambia al mover el mensaje. Hay que generar blobIds propios (hash de contenido o id sintético) y mantener el mapeo, más una caché local del contenido para no golpear IMAP en cada descarga de adjunto.

También: `Email/get` con `bodyValues` requiere haber parseado MIME. Hacer FETCH de partes bajo demanda contra IMAP en el request path es lento; la Opción B implica **almacenar cuerpos/estructura parseados en nuestra base**, que es justamente el punto de tener base propia.

### 5.6 Tiempo real: IDLE / NOTIFY

- **IDLE**: una conexión por carpeta seleccionada. Para N usuarios × M carpetas, eso escala mal en conexiones. Es el modelo clásico.
- **NOTIFY (RFC 5465)**: permite pedir notificaciones de **varias** carpetas sobre una sola conexión, sin polling. Mucho mejor para un sync engine multi-cuenta. Dovecot soporta NOTIFY.

Estrategia pragmática y estándar: **IDLE/NOTIFY sobre INBOX (+ carpetas activas) + polling con QRESYNC** en el resto con backoff. Cuando se detecta cambio → aplicar a la base → bump del state → emitir push (SSE) a los clientes conectados. Nótese que esto es idéntico se exponga JMAP o API propia.

### 5.7 Escritura y coherencia bidireccional

Los cambios del usuario en el webmail deben propagarse a IMAP (para que el móvil por IMAP los vea) y viceversa. Eso implica una cola de operaciones con reintentos, idempotencia y resolución de conflictos (ej: mail borrado desde otro cliente mientras nosotros le seteamos un flag → tolerar el fallo sin romper el state).

⚠️ Este es probablemente **el riesgo de ingeniería más alto de todo el proyecto**, y es completamente ortogonal a la elección de API.

---

## 6. Riesgos de elegir JMAP

Por honestidad, los contras reales:

1. **Superficie del Email object.** Es la parte más pesada de RFC 8621. Las múltiples formas de header y `bodyStructure`/`bodyValues` son mucho detalle para hacer bien.
2. **`queryChanges` es difícil.** Es la operación más compleja de la spec. Mitigación válida y usada: devolver `cannotCalculateChanges` y forzar refetch de la query — legítimo según la spec.
3. **Ecosistema móvil delgado.** El "gratis" descansa sobre Twake Mail. Ltt.rs está estancado. No hay que vender esto como si hubiera diez apps.
4. **Clientes de terceros exponen nuestros bugs.** Un cliente que no controlamos usará caminos de la spec que no probamos. Es a la vez el mayor beneficio (testing real) y una fuente de tickets.
5. **Calendars sigue en draft.** Si el proyecto creciera hacia calendario, ese terreno se mueve. Mitigación: mantener SOGo/CalDAV para calendario.
6. **Rigidez.** Si necesitamos un feature de producto que JMAP no modela, hay que extender con una capability propia (`urn:gruponu:...`) — el mecanismo existe y es correcto, pero es trabajo extra.

---

## 7. Matriz de decisión

| Criterio | JMAP completo | **JMAP subset (fases)** | API propia |
|---|---|---|---|
| Esfuerzo inicial | Alto (12-18 meses razonables) | **Medio** | Medio-bajo |
| Esfuerzo del sync engine (el grueso) | Idéntico | **Idéntico** | Idéntico |
| Diseño de API | Ya resuelto por la RFC | **Ya resuelto** | A inventar (y a equivocarse) |
| Clientes móviles de terceros | Sí | **Sí (Twake Mail)** | ❌ Nunca |
| Webmails de terceros como fallback | Sí | **Sí (Bulwark, TMail)** | ❌ No |
| Librería cliente para nuestro PWA | Gratis | **Gratis (jmap-jam/jmap-kit)** | A escribir y mantener |
| Tests de conformidad externos | Sí (JMAP TestSuite) | **Sí (parcial)** | ❌ Solo los nuestros |
| Implementación de referencia para copiar | jmap-perl, Stalwart | **jmap-perl (MIT, vivo)** | Ninguna |
| Portabilidad a otro backend (ej. Stalwart) | Sí | **Sí** | ❌ No |
| Libertad de modelar features propias | Baja | **Media (capabilities)** | Alta |
| Riesgo de sobre-ingeniería | **Alto** | Bajo | Bajo |
| Valor del proyecto como open source | Alto | **Alto** | Bajo |
| Deuda técnica a 3 años | Baja | **Baja** | Alta (API propia = mantenimiento eterno) |

---

## 8. Recomendación

**Exponer JMAP estándar, implementado como subset por fases, con la Fase 1+2 como objetivo de v1.**

Los cuatro argumentos que sostienen la decisión:

1. **El costo diferencial es chico y el trabajo pesado es común.** El sync engine IMAP→base propia con deltas incrementales, threading y reconciliación bidireccional es ~75-80% del proyecto y hay que hacerlo igual. JMAP agrega ~20-25% en la capa de serialización — a cambio de eliminar por completo la fase de *diseñar* una API, que es donde las APIs propias acumulan errores caros.

2. **Existe un plano probado.** `jmap-perl` es MIT, está vivo (push mayo 2026), pasa 132/132 tests de la suite oficial contra Cyrus, y su arquitectura (worker por cuenta + SQLite por cuenta + `firstsync`/`sync_imap` + SSE) es literalmente nuestra Opción B. Además publica sus gaps aceptables, que es información de ingeniería valiosísima: nos dice de antemano qué se puede recortar sin romper clientes.

3. **Opcionalidad estratégica.** Con JMAP, el webmail sirve contra Mailcow *y* contra Stalwart *y* contra cualquier backend JMAP futuro. Ganamos clientes móviles (Twake Mail), webmails de fallback (Bulwark/TMail — que ya está demostrado que corren sobre un proxy IMAP), herramientas de backup/migración (Vandelay, mujmap) y librerías TS mantenidas para nuestro propio frontend. Con API propia, todo eso es cero, permanentemente.

4. **Nadie implementa JMAP al 100%, y el estándar lo previó.** Cyrus, James y jmap-perl están todos incompletos y todos en producción. El mecanismo `using`/capabilities está diseñado para negociar exactamente esto. Elegir "subset" no es hacer trampa: es el uso normal del estándar.

**Contra qué decidimos:** una API propia sería ligeramente más rápida al principio y más flexible para features exóticos, pero convierte cada cliente futuro en trabajo propio y nos ata a mantener un SDK, una versión de API y una documentación que hoy nos regalan. El ahorro inicial se paga con intereses.

**Plan de implementación sugerido:**
- **Fase 0 — validación (bajo costo, alto valor):** levantar `jmap-perl` en Docker contra una cuenta de prueba de nuestro Mailcow y conectarle Twake Mail y/o Bulwark. Esto **valida empíricamente en días** que JMAP-sobre-Dovecot funciona en nuestro entorno, antes de escribir una línea. Si falla, lo sabemos barato. *(Documental: no ejecutado en este research.)*
- **Fase 1:** sync engine + JMAP read-only (Session, Mailbox/*, Email/get+query+changes, Thread/*, blobs, batching + back-references). Objetivo verificable: **un cliente JMAP de terceros lee mail de nuestro servidor.**
- **Fase 2:** escritura (`Email/set`, `Mailbox/set`), envío (`EmailSubmission/set`), push SSE. Ahí el webmail propio es usable.
- **Fase 3:** SearchSnippet, VacationResponse, **SieveScript (RFC 9661 → ManageSieve de Dovecot, alto valor/bajo costo para Mailcow)**, Quotas, WebSocket, PushSubscription móvil.
- **Transversal:** correr el JMAP TestSuite desde el día uno como CI. Es un oráculo de conformidad externo y gratuito — exactamente lo que una API propia nunca tendría.

**Decisión explícita a registrar:** declarar desde el inicio que el Email vive en **una sola Mailbox** (semántica IMAP), como hacen jmap-perl y Cyrus. Evita el problema más espinoso del mapeo sin costo práctico.

---

## 9. Fuentes

**Especificaciones**
- JMAP Specifications (índice oficial): https://jmap.io/spec/index.html
- RFC 8620 — JMAP Core: https://tools.ietf.org/html/rfc8620 · https://jmap.io/spec/rfc8620/
- RFC 8621 — JMAP for Mail: https://www.rfc-editor.org/rfc/rfc8621.html · https://datatracker.ietf.org/doc/html/rfc8621
- RFC 8887 — JMAP over WebSocket: https://rfc-editor.org/rfc/rfc8887
- RFC 9661 — JMAP for Sieve Scripts: https://www.rfc-editor.org/rfc/rfc9661.html
- RFC 7162 — IMAP CONDSTORE/QRESYNC: https://www.rfc-editor.org/rfc/rfc7162.html
- draft-ietf-jmap-calendars-27: https://datatracker.ietf.org/doc/draft-ietf-jmap-calendars/
- draft-ietf-jmap-essential-01 (EXPIRADO): https://datatracker.ietf.org/doc/html/draft-ietf-jmap-essential-01
- IETF JMAP WG: https://datatracker.ietf.org/wg/jmap/history/

**Implementaciones**
- JMAP Software Implementations: https://jmap.io/software/
- awesome-jmap: https://github.com/bonjourservices/awesome-jmap
- jmap-perl (proxy IMAP→JMAP, MIT, activo): https://github.com/jmapio/jmap-perl
- Demo del proxy: https://proxy.jmap.io/
- jmap-proxy-python (inactivo desde 2021): https://github.com/filiphanes/jmap-proxy-python
- stalwartlabs/imap-to-jmap (ARCHIVADO, dirección inversa): https://github.com/stalwartlabs/imap-to-jmap
- Stalwart: https://github.com/stalwartlabs/stalwart · https://stalw.art/blog/roadmap/ · https://stalw.art/blog/jmap-collaboration/ · https://stalw.art/docs/development/rfcs/
- Cyrus JMAP: https://www.cyrusimap.org/imap/developer/jmap.html
- Apache James JMAP config: https://james.apache.org/server/config-jmap.html
- JAMES-2884 (conformidad RFC 8620/8621): https://issues.apache.org/jira/browse/JAMES-2884
- James ADR JMAP new specs: https://github.com/apache/james-project/blob/master/src/adr/0018-jmap-new-specs.md
- James ADR 0047 push sobre WebSocket: https://apache.googlesource.com/james-project/+/master/src/adr/0047-jmap-push-over-websockets.md

**Dovecot / Mailcow**
- Dovecot: JMAP support in v2.3 (LWN, histórico): https://lwn.net/Articles/687159/
- JMAP Support Status (lista Dovecot): https://dovecot.org/mailman3/archives/list/dovecot@dovecot.org/thread/T2JX2REPW5R4M3II7VGM5DC72ISHN777/
- JMAP support in Dovecot: https://dovecot.org/mailman3/archives/list/dovecot@dovecot.org/thread/WC4XYXP3K75QAK6Q5EKGVBGIJLPI7HLY/
- mailcow issue #3133 (JMAP support, cerrado): https://github.com/mailcow/mailcow-dockerized/issues/3133
- Dovecot core releases: https://github.com/dovecot/core/releases

**Clientes y librerías**
- Twake Mail (Google Play): https://play.google.com/store/apps/details?id=com.linagora.android.teammail
- Ltt.rs (Google Play): https://play.google.com/store/apps/details?id=rs.ltt.android
- Ltt.rs (Codeberg): https://codeberg.org/iNPUTmice/lttrs-android
- jmap-jam: https://github.com/htunnicliff/jmap-jam · https://www.npmjs.com/package/jmap-jam
- jmap-kit: https://github.com/lachlanhunt/jmap-kit
- jmap-client-ts (Linagora): https://github.com/linagora/jmap-client-ts
- linagora/jmap-client (⚠️ draft viejo): https://github.com/linagora/jmap-client
- jmap-client (Stalwart, Rust): https://github.com/stalwartlabs/jmap-client
- jmap-mcp: https://github.com/rotkonetworks/jmap-mcp

**Contexto**
- Fastmail — open source JMAP proxy: https://www.fastmail.com/blog/an-open-source-jmap-proxy-javascript-library-and-webmail-demo/
- Fastmail — Making Email More Modern With JMAP: https://www.fastmail.com/blog/jmap-new-email-open-standard/
- Wikipedia — JMAP: https://en.wikipedia.org/wiki/JSON_Meta_Application_Protocol
