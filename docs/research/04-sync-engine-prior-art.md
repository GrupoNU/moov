# 04 — Sync Engine: Prior Art y Decisiones de Diseño

> Research documental (web + GitHub) para el webmail open source de Grupo NU sobre Mailcow.
> Foco: la **Opción B** — sync engine backend que sincroniza IMAP/Dovecot a base propia con
> índice full-text, sirviendo una API moderna a una PWA.
>
> **Fecha:** 2026-08-07
> **Autor:** research agent (VPS_Mail)
> **Estado:** borrador para decisión de arquitectura
> **Alcance:** 100% documental. Nada de lo acá afirmado fue probado en el VPS.

---

## 0. Resumen ejecutivo

El sync engine es la pieza de mayor riesgo del proyecto. La historia del prior art es consistente
y poco piadosa: **casi todos los intentos de "IMAP → base propia → API moderna" que apuntaron a
multi-tenant murieron o se volvieron producto cerrado** (Nylas), y los que sobrevivieron lo
hicieron **restringiendo el alcance** — un proceso por cuenta y cache parcial (Mailspring), o
sync de un solo folder con semántica de chat (Delta Chat), o renunciando al sync bidireccional
completo (notmuch + mbsync).

Nuestra ventaja estructural respecto de todos ellos: **controlamos el servidor IMAP**. Mailcow
corre Dovecot, que soporta CONDSTORE/QRESYNC/IDLE de forma confiable y homogénea. No tenemos que
lidiar con el zoo de servidores IMAP rotos que mató a Nylas. Eso reduce el riesgo del sync engine
en un orden de magnitud, y **debe ser explotado como decisión de diseño explícita**: el engine se
escribe contra Dovecot, no contra "IMAP genérico".

Recomendación resumida (detalle en §6):
**Go + PostgreSQL (índice + metadata + bodies parseados) + blobs MIME en filesystem + FTS con
tsvector para MVP, con puerta de salida a Meilisearch.** No guardar el `.eml` crudo duplicado en
la DB. No adoptar SQLite para multi-buzón. No reescribir en Rust salvo que el perfil de equipo
cambie.

---

## 1. Sync engines IMAP: prior art

### 1.1 Nylas Sync Engine (Python, MySQL) — **muerto, archivado 2026-06-25**

El caso de estudio más directo: era exactamente lo que queremos construir — "a RESTful API on top
of a powerful email sync platform", IMAP/SMTP adentro, JSON afuera.

**Arquitectura conocida:**
- Python + MySQL + Alembic para migrations, SQLAlchemy como ORM.
- Modelo de datos: `Account`, `Folder`, `Thread`, `Message`, `Block` (attachments), `Contact`,
  `Event`. La separación `Message`/`Block` es la decisión de schema más reutilizable: los cuerpos
  y adjuntos viven como blobs referenciados, no dentro de la fila del mensaje.
- Un proceso de sync por cuenta, orquestado por `inbox-sync start/stop`.
- Backend de deployment con ProxySQL delante de MySQL (indicio fuerte de que la capa de datos fue
  el cuello de botella real).

**Qué falló — y qué aprender:**

| Problema documentado | Lección para nosotros |
|---|---|
| **Sin soporte Exchange** en la versión open source. El diferencial pago era justamente el conector difícil. | El valor comercial de un sync engine está en los conectores raros. Nosotros no los necesitamos: solo Dovecot. |
| **Credenciales sin cifrar en MySQL** ("Passwords and OAuth tokens are stored unencrypted in the local MySQL data store"). Reconocido en el README como limitación de diseño. | Cifrado de credenciales at-rest **desde el día 1**, no como deuda. Ver §3.4. |
| **Initial sync lentísimo** ("can take quite a while depending on how much mail you have"). Issues abiertos de "Incomplete sync" que nunca se cerraron. | El backfill inicial es el killer de UX. Diseñar sync incremental por prioridad (headers primero, bodies después, backfill en background). Ver §4.7. |
| **Abandono del modelo self-hosted**: el aviso de archivo dice que las capacidades "live on in the fully-hosted Nylas API — no self-hosting, no Vagrant, no infrastructure to run". | Nylas no murió por incompetencia técnica: murió porque **operar sync IMAP multi-proveedor a escala no es rentable como open source**. Nuestro alcance (decenas de buzones, un solo servidor conocido) es un problema categóricamente distinto y tratable. |

**Veredicto:** Nylas es la advertencia, no el modelo. Su schema (`Message`/`Thread`/`Block`/`Folder`)
sí es un buen punto de partida. Su ambición de universalidad es lo que hay que evitar.

Fuentes: <https://github.com/nylas/sync-engine>, <https://github.com/nylas/sync-engine/blob/master/README.md>

---

### 1.2 Mailspring / Mailspring-Sync (C++11 + MailCore2 + SQLite) — **la referencia más relevante**

Arquitectura casi idéntica a lo que proponemos, pero local-first en vez de server-side. Es el
prior art del que más se puede copiar.

**Arquitectura:**
- **Un proceso `mailsync` por cuenta de email.** Aislamiento total; matar un proceso no corrompe
  datos gracias a transacciones SQLite.
- **Dos threads con conexiones IMAP separadas:**
  - *Background worker*: escanea folders periódicamente. Usa **"CONDSTORE / XYZRESYNC to check for
    mail"** y decide entre un **"local sync"** (barato, solo cambios) o un **"deep sync"**
    (costoso, reconciliación completa) según lo que soporte el servidor.
  - *Foreground worker*: mantiene **IDLE** sobre el folder principal, despierta ante cambios y
    ejecuta tareas iniciadas por el usuario (fetch de body, etc.).
- **IPC por stdin/stdout con JSON newline-delimited.** El motor emite objetos modificados a stdout;
  la UI Electron los consume reactivamente. Las tareas entran por stdin.
- **Modelo de tareas dividido en dos fases** — patrón que deberíamos adoptar tal cual:
  - *Local operation*: modifica la DB inmediatamente → la UI actualiza al instante (optimistic UI).
  - *Remote operation*: la acción de red, encolada en una tabla `tasks` de SQLite, **reintentable**.

**Decisiones de schema notables:**
- **"Fat table" pattern**: cada fila tiene una columna `data` con el JSON completo del modelo,
  *más* columnas individuales duplicadas para los campos que se necesitan consultar.
  - *Ventaja reconocida:* migrations triviales, agregar campos es gratis.
  - *Costo reconocido:* filas grandes, SCAN queries lentas.
  - *Nuestro veredicto:* con PostgreSQL esto se resuelve mejor con `JSONB` + columnas generadas
    indexadas. No hace falta duplicar a mano.
- **Cache parcial deliberado**: **solo se bajan los bodies completos de los últimos 3 meses**;
  los mensajes más viejos guardan solo headers. Esta es *la* decisión que hace viable un buzón de
  100 GB. Debe ser nuestra decisión también.
- **IDs estables por hash de headers** — y su falla documentada: *"in rare cases, two messages in
  an account can have the same ID and Mailspring will only show one."* Un hash de headers **no es
  una clave primaria segura**. Ver riesgo R3 en §8.
- **Gmail**: ignora las virtual label folders, sincroniza solo Spam/All Mail/Trash y usa
  `X-GM-LABELS`. Irrelevante para nosotros (Dovecot), pero muestra el costo de cada proveedor extra.

**Qué salió mal:** el proyecto está documentado como estancado en desarrollo, con bugs persistentes
reportados por la comunidad pese a buen rendimiento. Hay un bug ilustrativo — *"too many SQL
variables"* en sync de contactos — que es el síntoma clásico de batchear sin límite contra SQLite.

Fuentes: <https://github.com/Foundry376/Mailspring-Sync>,
<https://github.com/Foundry376/Mailspring-Sync/blob/master/README.md>,
<https://foundry376.github.io/Mailspring/guides/Database.html>,
<https://github.com/Foundry376/Mailspring/issues/1951>,
<https://biggo.com/news/202502040723_mailspring-development-concerns>

---

### 1.3 Delta Chat core (Rust) — sync IMAP robusto en condiciones hostiles

Delta Chat (hoy `chatmail/core`) usa email como transporte de chat. Su sync es minimalista pero
**extremadamente probado** contra servidores IMAP del mundo real.

**Lo relevante:**
- Trackea `uid_next` y `uidvalidity` por folder, con funciones explícitas `set_uid_next` /
  `set_uidvalidity` (RFC 3501 §2.3.1.1).
- **Bug instructivo (issue #2188):** `last_seen_uid`/`uid_next` solo se actualizaba si el FETCH
  devolvía al menos un resultado. Eso rompe cuando **un proveedor rebobina UIDNEXT sin cambiar
  UIDVALIDITY** — comportamiento fuera de spec pero real. La corrección: si UIDNEXT observado es
  menor que el guardado, rebobinar el estado local; y persistir `uid_next - 1` **siempre**, haya o
  no resultados.
  → **Lección directa:** el estado de sync no puede derivarse solo de los mensajes vistos. Hay que
  persistir el `UIDNEXT`/`HIGHESTMODSEQ` anunciados por el servidor como hecho independiente.
- **CONDSTORE sí, QRESYNC no (decisión explícita)**: el issue #2941 razona que "CONDSTORE allows to
  quickly synchronize message flags, and QRESYNC extends CONDSTORE to quickly synchronize expunged
  messages. Since Delta Chat is not interested in expunged messages, for better compatibility it is
  enough to support CONDSTORE." → **Nosotros sí necesitamos QRESYNC**, porque un webmail tiene que
  mostrar borrados hechos desde otro cliente. Pero la observación de compatibilidad importa:
  QRESYNC tiene menos soporte que CONDSTORE en el ecosistema (irrelevante en Dovecot, que lo
  soporta bien).
- **IDLE con timeout agresivo (issue #5093):** reducen el timeout de IDLE y lo resetean con cada
  keepalive `OK Still here` del servidor. Las conexiones IDLE mueren silenciosamente todo el tiempo
  (NAT, middleboxes); confiar en los 29 minutos del RFC es garantía de perder eventos.

Fuentes: <https://github.com/chatmail/core>, <https://rs.delta.chat/deltachat/imap/index.html>,
<https://github.com/chatmail/core/issues/2188>, <https://github.com/chatmail/core/issues/2941>,
<https://github.com/deltachat/deltachat-core-rust/issues/5093>,
<https://github.com/chatmail/core/blob/main/standards.md>

---

### 1.4 mbsync/isync vs OfflineIMAP — el problema del mapeo de UIDs

Ambos son sincronizadores IMAP ↔ Maildir. La diferencia entre ellos es una lección de modelado de
datos que nos aplica de lleno.

- **OfflineIMAP** (Python): **fuerza el esquema de UIDs del remoto sobre el store local**. Un solo
  set de `uidvalidity`/UIDs. Simple, pero acopla el estado local a la numeración del servidor: si
  UIDVALIDITY cambia, hay que re-descargar todo (issue #190, "optimize re-download on UIDVALIDITY
  changes" — abierto largamente).
- **mbsync/isync** (C): **enumera el store local por separado**, manteniendo **dos sets de UIDs que
  hay que mapear** entre sí. Más complejo, pero desacopla: un cambio de UIDVALIDITY remoto no
  destruye el estado local, solo invalida el mapeo. Guarda UIDVALIDITY en un archivo
  `.uidvalidity` y codifica los UIDs en los nombres de archivo.
- **Rendimiento:** mbsync es medido consistentemente como más rápido que OfflineIMAP en runs de
  sincronización completa.

**Decisión que esto nos impone:** nuestro `message.id` interno **debe ser independiente del UID
IMAP**. El UID es un atributo mutable de la relación (mensaje, folder, uidvalidity-epoch), no la
identidad del mensaje. Ver el schema en §7.

Fuentes: <https://anarc.at/blog/2021-11-21-mbsync-vs-offlineimap/>,
<https://vxlabs.com/2019/07/05/mbsync-vs-offlineimap-speed/>,
<https://github.com/OfflineIMAP/offlineimap/issues/190>,
<https://www.mail-archive.com/isync-devel@lists.sourceforge.net/msg01778.html>

---

### 1.5 notmuch (+ Xapian) y lieer — el modelo "storage inmutable + metadata aparte"

- **notmuch** indexa colecciones Maildir/MH con **Xapian**. Su decisión central: **el Maildir es
  storage inmutable; toda la metadata (tags) vive en la base Xapian**. Los tags son arbitrarios y
  los "built-in" (sender, subject) se tratan igual que los definidos por el usuario.
- **lieer** hace sync bidireccional de tags entre notmuch y Gmail — pero **usando la API de Gmail,
  no IMAP**, explícitamente porque el label syncing por IMAP es un infierno.

**Lección:** la separación **blob inmutable / metadata mutable** es el patrón correcto y lo
adoptamos. El `.eml` crudo nunca cambia; flags, labels, read-state y threading son metadata
mutable en la DB relacional. Esto habilita dedupe, cifrado y re-indexado sin tocar los blobs.

**Contra-lección:** que lieer haya renunciado a IMAP para sincronizar labels indica que el **sync
bidireccional de flags es el subproblema más difícil**. Nosotros lo tenemos más fácil (Dovecot,
keywords IMAP estándar, CONDSTORE), pero no es gratis. Ver §4.6.

Fuentes: <https://notmuchmail.org/>, <https://github.com/gauteh/lieer>,
<https://www.kmjn.org/notes/unix_style_mail_tools.html>

---

### 1.6 Thunderbird y meli — notas breves

- **Thunderbird**: su global search usa un índice separado (Gloda, sobre SQLite) del store de
  mensajes (mbox/maildir). La lección histórica de Thunderbird es negativa: mantener **dos fuentes
  de verdad divergentes** (el store y el índice) produjo años de bugs de "search doesn't find my
  mail" y reconstrucciones de índice. **El índice debe ser derivable y reconstruible desde la
  fuente de verdad, y esa reconstrucción debe estar automatizada y ser barata.**
- **meli** (Rust, TUI): implementación moderna y limpia de cliente IMAP, útil como referencia de
  código para el manejo de la máquina de estados IMAP, pero su modelo de datos es de cliente
  single-user, no de servidor multi-buzón. Valor: referencia de implementación, no de arquitectura.

---

### 1.7 Stalwart — el "qué pasa si no construimos nada"

Stalwart es un mail server all-in-one en Rust (IMAP, JMAP, SMTP, POP3, CalDAV/CardDAV), organizado
como workspace Cargo de ~25 crates especializados. Su **FTS es configurable**: motor interno
(índice bloom-filter, 17 idiomas, sin servicio extra) para deployments chicos/medianos, o delegado
a Meilisearch / Elasticsearch / OpenSearch / PostgreSQL para cargas grandes.

**Por qué importa:** Stalwart ya resolvió "JMAP server-side sobre almacenamiento propio". Es la
alternativa estratégica a construir un sync engine: *reemplazar* Mailcow por Stalwart y hacer solo
la PWA contra JMAP. Esa opción debe evaluarse en el documento de arquitectura general — acá solo
se deja constancia de que su gradiente de FTS (interno → Meilisearch → Elastic) **valida nuestro
camino de crecimiento propuesto en §3.5**.

Fuentes: <https://stalw.art/architecture/>, <https://github.com/stalwartlabs/stalwart>

---

## 2. Librerías Go: estado 2026

### 2.1 `emersion/go-imap` v2 — ⚠️ **el hallazgo crítico del research**

| Ítem | Estado verificado |
|---|---|
| Última release | **v2.0.0-beta.8 — 7 de febrero de 2025** |
| Tiempo sin release | **~18 meses** a la fecha de este informe |
| Estabilidad declarada | Sin v2.0.0 estable. pkg.go.dev marca explícitamente **no estable** ("When a project reaches major version v1 it is considered stable" — no cumplido) |
| Nota del autor | Desde beta.1 (feb 2023): *"The API hasn't stabilized yet but no more major/invasive changes are expected"* |

**Extensiones soportadas por `imapclient` v2** (documentadas): IDLE, **CONDSTORE**, **QRESYNC**,
**NOTIFY**, MOVE, UIDPLUS, ESEARCH, SORT, THREAD, METADATA, OBJECTID, COMPRESS, ACL, NAMESPACE, ID,
UNAUTHENTICATE, LIST-EXTENDED, LIST-STATUS.

**Lectura honesta:** la cobertura de extensiones es *exactamente* la que necesitamos —
QRESYNC + CONDSTORE + IDLE + NOTIFY + MOVE + UIDPLUS es la lista completa de lo que un sync engine
serio requiere, y ninguna otra librería Go se le acerca. Pero **beta hace 18 meses es una señal de
riesgo real de mantenimiento**, no una formalidad. La API va a estar bien; el riesgo es quedarnos
sin upstream para bugs.

**Mitigación recomendada (obligatoria, no opcional):**
1. Vendorizar `go-imap/v2` desde el primer commit y pinnear el hash.
2. **Encapsular el 100% del acceso IMAP detrás de una interfaz propia** (`imapclient.Client` nunca
   se toca fuera del paquete `internal/imap`). Si hay que forkear o reemplazar, el blast radius es
   un paquete.
3. Presupuestar tiempo para mantener un fork propio. Go-imap es una librería legible y acotada;
   forkear es viable, no catastrófico.

Fuentes: <https://github.com/emersion/go-imap/releases>,
<https://pkg.go.dev/github.com/emersion/go-imap/v2/imapclient>,
<https://github.com/emersion/go-imap/blob/v2/capability.go>

### 2.2 Parsing MIME: `go-message` vs `enmime`

| | `emersion/go-message` | `jhillyerd/enmime` |
|---|---|---|
| Modelo | **Streaming** (RFC 5322, 2045, 2046, 2047, 2183) | Construye **árbol completo de `MIMEPart` en memoria** |
| Charsets | Paquete `charset` explícito; `import _ ".../charset"` habilita todos. **Non-UTF-8 solo en lectura** | Decodificación integrada al pipeline |
| Decoding | quoted-printable / base64 automáticos | Decodifica antes de almacenar en el `MIMEPart` |
| Madurez | Mismo autor que go-imap (coherencia de ecosistema) | Autodescrito **"production quality"**, con la advertencia: *"there are many buggy MIME encoders in the wild, so you may still encounter messages it cannot parse"* |

**Recomendación:** **usar ambos.** `go-message` como parser primario (streaming = memoria acotada
en mails de 40 MB con adjuntos, y coherencia con go-imap), con **`enmime` como fallback**: si
`go-message` falla en un mensaje, reintentar con `enmime` antes de marcarlo como
`parse_failed`. Los dos parsers fallan en conjuntos distintos de mails rotos. Ver riesgo R4.

Fuentes: <https://github.com/emersion/go-message>, <https://github.com/jhillyerd/enmime>,
<https://github.com/emersion/go-message/blob/master/charset/charset.go>

### 2.3 Resto del ecosistema Go

- **`emersion/go-smtp`** y **`emersion/go-sasl`**: maduros, mismo autor, sin observaciones. Los
  usamos para el envío (submission a Mailcow) y auth SASL.
- **JMAP server-side en Go: no existe nada usable.**
  - `foxcpp/go-jmap`: implementa JMAP Core contra **draft-ietf-jmap-core-17 (marzo 2019)** —
    un draft, no el RFC final. Marcado WIP. **Abandonado.**
  - `~rockorager/go-jmap` (sr.ht): fork de foxcpp con refactor masivo. Cubre core (incl.
    PushSubscription y EventSource), mail, smime-verify, MDN. **Es una librería de cliente**, no un
    servidor. Es la mejor opción Go si algún día queremos hablar JMAP, pero no nos da un servidor.
  - Servidores JMAP reales: Stalwart, Cyrus, Fastmail, Apache James — **ninguno en Go**.

  → **Decisión: nuestra API es REST/JSON propia, no JMAP.** Diseñarla *inspirada* en la semántica
  JMAP (`/changes` con `sinceState`, `/query` con filtros, `/set` con create/update/destroy) para
  dejar la puerta abierta, pero sin comprometerse a la spec. Implementar JMAP server-side desde
  cero en Go es un proyecto en sí mismo.

Fuentes: <https://github.com/foxcpp/go-jmap>, <https://sr.ht/~rockorager/go-jmap/>,
<https://jmap.io/software/>

### 2.4 La alternativa honesta: ¿Rust o TypeScript en vez de Go?

**Argumentos reales a favor de Rust:**
- El prior art más robusto en sync IMAP moderno es Rust: `chatmail/core` (años de batalla contra
  servidores reales) y Stalwart (mail server completo). Hay código de referencia de calidad.
- Ownership elimina toda una clase de bugs de concurrencia en un sistema que va a tener decenas de
  goroutines/tasks por buzón.
- Rendimiento y footprint de memoria superiores en parsing masivo — importa en el backfill de un
  buzón de 100 GB.

**Argumentos reales en contra de Rust (decisivos acá):**
- **No hay equivalente Rust a go-imap v2 con QRESYNC + NOTIFY + CONDSTORE como librería cliente
  reutilizable.** El código de Delta Chat y Stalwart está acoplado a sus productos. Usaríamos
  `async-imap`, que cubre menos extensiones. Cambiaríamos un riesgo de mantenimiento por un
  riesgo de funcionalidad faltante — peor trato.
- Velocidad de desarrollo y curva de aprendizaje: el proyecto es de infra interna, no un producto
  con presupuesto de I+D.

**Argumentos sobre TypeScript/Node:**
- A favor: un solo lenguaje con el frontend; `imapflow` (de nodemailer) es una librería IMAP
  cliente sorprendentemente buena y activamente mantenida; `mailparser` es el parser MIME más
  probado del ecosistema open source por volumen de uso.
- **En contra, decisivo:** el sync engine es un servicio de larga vida, con muchas conexiones
  persistentes, parsing CPU-bound y manejo de blobs grandes. Es exactamente el perfil de carga
  donde el modelo single-threaded event-loop de Node sufre, y donde el GC de V8 con buffers
  grandes se vuelve un problema operativo. Además Node obligaría a worker threads o multiproceso
  para el parsing, reconstruyendo a mano lo que Go da gratis.

**Veredicto: Go.** No porque sea el mejor lenguaje en abstracto, sino porque `go-imap/v2` es la
única librería cliente IMAP del ecosistema open source que cubre QRESYNC + CONDSTORE + IDLE +
NOTIFY con una API limpia, y porque el perfil de concurrencia (N conexiones IMAP persistentes +
workers de parsing + API HTTP) es literalmente el caso de uso canónico de Go. El riesgo de
mantenimiento de go-imap se mitiga con vendoring + encapsulación (§2.1); es un riesgo acotado y
gestionable, a diferencia del riesgo abierto de "nos falta una extensión IMAP" en Rust.

---

## 3. Almacenamiento e indexación

### 3.1 La pregunta central: ¿mail completo en la DB, o índice + on-demand?

**Ninguno de los dos extremos.** El prior art converge en una tercera opción, y Mailspring la
ejecuta explícitamente: **cache parcial por recencia**.

| Estrategia | Problema |
|---|---|
| Todo el mail en la DB | Un buzón de 100 GB × N buzones. Duplicamos el storage de Mailcow. Backup y restore se vuelven inmanejables. Descartado. |
| Solo índice + bodies on-demand del IMAP | Cada apertura de mail es un round-trip IMAP con latencia. Peor: la búsqueda full-text **necesita el body**, así que igual hay que parsear todo alguna vez. Búsqueda offline en la PWA: imposible. Descartado. |
| **Híbrido por recencia (recomendado)** | Complejidad de gestión del cache. Aceptable. |

**Diseño concreto propuesto:**

1. **Siempre y para todos los mensajes** (sin importar antigüedad):
   - Headers parseados → tabla `messages` (from, to, cc, subject, date, message_id, in_reply_to,
     references, flags, size, estructura MIME resumida).
   - **Texto plano extraído** del body → tabla `message_search` para el índice FTS. Es
     típicamente el 1-3% del tamaño del mensaje. Un buzón de 100 GB genera ~1-3 GB de texto
     indexable. Perfectamente manejable.
   - Metadata de adjuntos (nombre, mime type, tamaño, part id) — **sin el contenido**.

2. **Cache de bodies completos (HTML + texto) para mensajes recientes**: ventana configurable,
   default **6 meses** (Mailspring usa 3; con storage propio podemos ser más generosos).

3. **Blobs (`.eml` crudo y adjuntos): NO en la base.** Filesystem content-addressed
   (`blobs/<sha256[0:2]>/<sha256[2:4]>/<sha256>`), referenciados por hash desde la DB.
   - **Dedupe gratis**: un adjunto de 8 MB enviado a 12 buzones internos se guarda una vez. En una
     instancia corporativa esto es un ahorro enorme, no marginal.
   - Backup incremental trivial (los blobs son inmutables → rsync/restic son óptimos).
   - `refcount` en la DB para GC de blobs huérfanos.

4. **Adjuntos y mensajes fuera de la ventana de cache: fetch on-demand desde IMAP**, y al traerlos
   se promueven al cache (LRU con presupuesto de disco).

**Punto de apalancamiento clave:** el `.eml` crudo **ya está en Mailcow/Dovecot**. Somos un cache,
no el sistema de registro. Eso significa que **podemos borrar cualquier blob en cualquier momento**
y re-fetchearlo. Esta propiedad hace que el diseño sea robusto de un modo que Nylas (que era la
única copia para muchos usuarios) nunca pudo permitirse.

### 3.2 SQLite + FTS5 — descartado para nuestro caso

Excelente para single-tenant local-first (por eso Mailspring y notmuch lo usan). **No para
nosotros:**
- Nuestro caso es **multi-buzón en un servidor**: decenas de buzones, escrituras concurrentes desde
  N workers de sync + lecturas desde la API. Es el escenario donde SQLite es la elección
  equivocada — el consenso documentado es que si hay escritura concurrente desde múltiples
  workers, gestión de usuarios server-side, o crecimiento más allá de un nodo, corresponde
  PostgreSQL.
- La alternativa "una SQLite por buzón" es tentadora (aislamiento perfecto, backup por archivo)
  pero rompe todo lo transversal: búsqueda cross-buzón, dedupe global de blobs, tablas de admin.
- El bug de Mailspring *"too many SQL variables"* es un recordatorio de los límites duros de SQLite
  al batchear.

### 3.3 PostgreSQL + tsvector — **recomendado para MVP**

**A favor:**
- Ya está en el stack del grupo (Supabase self-hosted, PostgreSQL 17.6 en VPS_atmosfera). Cero
  tecnología nueva que operar, backupear y monitorear.
- `tsvector` + índice GIN cubre búsqueda por palabras con stemming y ranking. `pg_trgm` cubre
  búsqueda por substring/fuzzy en nombres y direcciones (el caso "escribo 'gonz' y quiero encontrar
  a González") — que tsvector **no** cubre bien y es más de la mitad de las búsquedas reales de un
  webmail.
- Transaccionalidad real entre metadata, estado de sync y cola de tareas. Esto no es un lujo: el
  patrón de tasks local/remote de Mailspring **necesita** transacciones para no corromper estado.
- `JSONB` + columnas generadas indexadas resuelve el "fat table" de Mailspring de forma limpia.

**En contra (documentado y honesto):** el rendimiento de tsvector **se degrada notablemente más
allá del orden de millones de registros**. Este es el límite real de la recomendación.

**Cálculo de escala para nuestro caso:** ~100 buzones × ~50.000 mensajes promedio = **~5M
mensajes**. Estamos **en el borde** de donde tsvector empieza a sufrir, no cómodamente adentro. Por
eso la recomendación viene con puerta de salida obligatoria (§3.5).

### 3.4 Cifrado at-rest

- **Credenciales IMAP: cifrado obligatorio desde el día 1.** Es la falla explícita de Nylas.
  Usar una master key en variable de entorno / systemd credential (nunca en la DB), AES-256-GCM
  por credencial. Idealmente, evitar guardar contraseñas: usar **XOAUTH2 o master password de
  Dovecot** con impersonación, para no custodiar las contraseñas de los usuarios en absoluto.
- **Blobs: cifrado opcional por buzón**, AES-256-GCM con key derivada por buzón. El diseño
  content-addressed **es incompatible con cifrado convencional** (el ciphertext difiere por key →
  se pierde el dedupe). Si se activa cifrado por buzón, el dedupe queda limitado a intra-buzón.
  → **Decisión de MVP:** blobs sin cifrar (el disco del VPS ya debería tener LUKS), dedupe global
  activo. Cifrado por buzón queda como feature de fase 2, con dedupe degradado y documentado.
- **DB: confiar en el cifrado de disco del VPS**, no en cifrado a nivel columna (que rompería el
  índice FTS).

### 3.5 Camino de crecimiento del indexer

Gradiente validado por la arquitectura de Stalwart (interno → Meilisearch → Elasticsearch):

```
Fase 1 (MVP)     PostgreSQL tsvector + pg_trgm
                 ~hasta 3-5M mensajes. Cero infra nueva.
                      ↓  (trigger: p95 de búsqueda > 500ms)
Fase 2           Meilisearch como índice externo
                 Postgres sigue siendo la fuente de verdad.
                 Typo-tolerance y ranking muy superiores. Operación simple.
                      ↓  (solo si multi-nodo o >50M mensajes — improbable)
Fase 3           Elasticsearch / OpenSearch
```

**Requisito de diseño que habilita esto:** desde el día 1, el indexado debe estar detrás de una
interfaz `Indexer` (`Index(msg)`, `Delete(id)`, `Search(query, filters) []MessageID`) y **debe
existir un comando `reindex` que reconstruya el índice completo desde la fuente de verdad**. Sin
eso, cambiar de motor es una migración; con eso, es un flag de configuración y una noche de
reindexado. Es también la mitigación al problema histórico de Thunderbird (§1.6).

**Descartados y por qué:**
- **bleve** (Go): tentador por ser in-process y nativo Go, pero su desarrollo es lento y el
  rendimiento en corpus grandes es inferior a Meilisearch/tantivy. No aporta sobre Postgres en
  fase 1 ni compite con Meilisearch en fase 2.
- **tantivy** (Rust): excelente motor, pero desde Go implicaría FFI/cgo o un servicio aparte —
  y si vamos a correr un servicio aparte, Meilisearch (que está construido *sobre* tantivy) da
  lo mismo con operación mucho más simple.

---

## 4. Los problemas duros

### 4.1 Threading

**JWZ (Zawinski, 1997)** sigue siendo el estándar de facto. Dos fases: (1) construir el árbol
siguiendo `Message-ID` / `In-Reply-To` / `References`; (2) podar y ordenar.

Elementos no negociables de la implementación:
- **Phantom containers**: si X referencia a Y y no tenemos Y, se crea un placeholder para Y y se
  trata como nodo. Sin esto, los hilos se fragmentan al recibir mensajes fuera de orden — lo cual
  es *el caso normal* durante un backfill.
- **Detección de ciclos**: JWZ chequea explícitamente antes de agregar una arista padre — si A→B
  haría que A sea su propio ancestro, se descarta la arista. Los mails con headers `References`
  corruptos existen y producen ciclos.
- **Fallback por subject**: para mensajes sin `In-Reply-To` ni `References` (clientes viejos,
  digests de listas), agrupar por subject normalizado (strip de `Re:`, `Fwd:`, `RE:`, `[Lista]`,
  y variantes locales — `Rif:`, `AW:`, `RV:` en español/alemán/italiano). **Riesgo documentado:
  puede fusionar conversaciones no relacionadas que comparten subject.**
  → **Mitigación práctica:** limitar el fallback por subject a una **ventana temporal** (p. ej. 30
  días) y **nunca cruzar buzones**. Un "Consulta" de enero y otro de agosto no son el mismo hilo.

**Decisión de implementación:** threading **incremental**, no batch. Recalcular todo el árbol de
hilos en cada mensaje nuevo es O(n) por mensaje. Guardar `thread_id` materializado en la tabla
`messages` y actualizarlo incrementalmente al insertar; con un job de reconciliación periódico
para los casos de merge de hilos (cuando llega el mensaje que une dos phantom trees).

Fuentes: <https://www.jwz.org/doc/threading.html>, <https://mboxviewer.net/glossary/threading/>,
<https://www.bowaggoner.com/bomail/writeups/threads.html>

### 4.2 MIME patológico y charset hell

- **Nunca confiar en el `Content-Type` declarado.** Encoding mal declarado es el caso más común de
  mojibake. Estrategia en cascada: (1) charset del header; (2) si falla o el resultado tiene
  bytes inválidos, **detección heurística** (`chardet`-equivalente en Go: `saintfish/chardet`);
  (3) fallback a `windows-1252` (no a UTF-8: 1252 decodifica cualquier byte, UTF-8 no) marcando el
  mensaje como `charset_guessed`.
- `go-message` solo soporta charsets non-UTF-8 **en lectura** — suficiente para nosotros
  (escribimos siempre UTF-8), pero hay que saberlo.
- **Casos patológicos a tener en la suite de tests desde el día 1:** multipart anidado a 10+
  niveles; `message/rfc822` embebido recursivamente; boundaries duplicados o no terminados;
  headers de 20 KB; RFC 2047 encoded-words partidos a la mitad de un carácter multibyte;
  `Content-Transfer-Encoding` mentido (base64 declarado, texto plano real); mails sin body;
  mails con CRLF inconsistente.
- **Regla operativa:** un mensaje que no parsea **nunca puede romper el sync del folder**. Se marca
  `parse_status = 'failed'`, se guarda el blob crudo, se sigue. La UI muestra "no se pudo procesar
  este mensaje — descargar original". Nylas tuvo issues de "Incomplete sync" abiertos
  indefinidamente; casi con seguridad ese es el mecanismo.

### 4.3 Sanitización de HTML

Este es el punto donde un bug es un **XSS en el contexto del webmail** — es decir, robo de toda la
sesión de correo del usuario. Investigadores de seguridad (PortSwigger, Sonar) han encontrado
bypasses de sanitizer en **Fastmail, ProtonMail y Gmail**. No es un problema resuelto.

**Arquitectura recomendada — defensa en profundidad de 3 capas:**

1. **Sanitización server-side** durante el parseo (Go: `bluemonday` con allowlist estricta).
   Genera el HTML almacenado. Ventaja: se hace una vez, no en cada render.
2. **Sanitización client-side con DOMPurify** antes de insertar en el DOM. Fastmail documenta la
   opción de sanitizar en el browser — las APIs permiten parsear HTML a un árbol DOM **sin ejecutar
   scripts ni disparar requests de red** antes de limpiarlo. Redundante a propósito.
3. **Aislamiento por `<iframe sandbox>` + CSP estricta.** Esta es la capa que realmente importa:
   aunque las capas 1 y 2 fallen, el HTML del mail se ejecuta en un origin sin acceso al de la
   aplicación. `sandbox="allow-popups allow-popups-to-escape-sandbox"` (sin `allow-scripts`,
   sin `allow-same-origin`) + `Content-Security-Policy: default-src 'none'; img-src <proxy>;
   style-src 'unsafe-inline'`.

**CSS es un vector, no decoración.** La investigación de PortSwigger ("CSS: the bomb inside your
inbox") y "Cascading Spy Sheets" documentan exfiltración y fingerprinting **solo con CSS**,
sin JavaScript: selectores de atributo + `background-image` con URLs condicionales permiten
filtrar contenido del mensaje al servidor del atacante. Consecuencias: eliminar `@import`,
`position: fixed`, y **cualquier propiedad que pueda generar una request de red condicional**;
scopear todo el CSS al contenedor del mensaje. La estrategia de Gmail de **strippear todo el CSS no
inlineado** es una respuesta directa a esta clase de ataque, y es la política más defendible.

### 4.4 Remote content: bloqueo y proxying

**Cómo lo hacen los grandes:**
- **Fastmail**: *"All off-site images embedded in emails are now proxied through our servers."*
  El email original queda intacto (importante: forwarding e IMAP muestran el mensaje como llegó).
  El servidor de imágenes ve una request desde la infra de Fastmail con user-agent genérico, no la
  IP ni ubicación del usuario. Beneficio adicional: todo llega por HTTPS, eliminando warnings de
  mixed-content. **Limitación que Fastmail admite explícitamente:** si la URL de la imagen tiene
  parámetros de tracking, el servidor remoto **igual puede deducir la identidad del usuario** por
  la mera carga — sigue siendo *"a marked improvement on not proxying at all"*, pero no es
  anonimato.
- **Gmail**: proxy de imágenes con caching agresivo (las carga una vez y sirve desde su cache),
  lo que rompe el pixel tracking de aperturas repetidas.
- **Proton**: proxying **incondicional** de recursos externos, específicamente como mitigación
  contra la exfiltración por CSS queries.

**Diseño para nosotros:**
- **Bloqueo por defecto** con banner "mostrar imágenes" (por remitente, con opción "confiar
  siempre en este remitente").
- Al desbloquear, **todo va por nuestro proxy**. URLs reescritas a
  `/proxy/img?u=<url_b64>&s=<hmac>` — el **HMAC es obligatorio** para que el endpoint no sea un
  SSRF/open-proxy abierto al mundo.
- El proxy debe: validar el HMAC, **rechazar IPs privadas/link-local/loopback tras resolver DNS**
  (anti-SSRF, incluyendo re-validación post-redirect para evitar DNS rebinding), limitar tamaño y
  timeout, forzar content-type de imagen, cachear agresivamente.
- **El blob original nunca se modifica.** Solo la representación HTML servida a la PWA.

Fuentes: <https://www.fastmail.com/blog/better-security-and-privacy-through-image-proxying/>,
<https://www.fastmail.com/blog/sanitising-html-the-dom-clobbering-issue/>,
<https://words.filippo.io/how-the-new-gmail-image-proxy-works-and-what-this-means-for-you/>,
<https://portswigger.net/research/css-the-bomb-inside-your-inbox>,
<https://roots.ec/blog/spy-sheets/>,
<https://www.sonarsource.com/blog/code-vulnerabilities-leak-emails-in-proton-mail/>,
<https://making.close.com/posts/rendering-untrusted-html-email-safely/>

### 4.5 Draft sync

El caso más subestimado y una fuente de bugs desproporcionada.
- IMAP no tiene "update message": editar un draft es **APPEND del nuevo + STORE \Deleted del
  viejo + EXPUNGE**. Cada guardado genera un UID nuevo.
- Sin cuidado, esto produce **decenas de copias del mismo draft** en el folder Drafts — el bug
  clásico de todo webmail.
- **Diseño:** el draft vive en nuestra DB como entidad de primera clase con ID propio y estable
  (`drafts` separada de `messages`). Se sincroniza a IMAP con **debounce** (p. ej. 30s de
  inactividad o al cerrar el composer), no en cada tecla. Se guarda el UID IMAP de la última
  versión persistida para poder borrarla al escribir la siguiente. Usar **UIDPLUS** (`APPENDUID`)
  para conocer el UID del nuevo draft sin tener que buscarlo — go-imap v2 lo soporta.
- Al enviar: APPEND a Sent + borrar el draft de Drafts, **en ese orden** (mejor un draft huérfano
  que un mail enviado sin copia en Sent).

### 4.6 Flags bidireccionales

El escenario: el usuario marca como leído desde su cliente IMAP del teléfono; nuestra PWA debe
enterarse. Y viceversa.

- **De IMAP hacia nosotros:** CONDSTORE es la herramienta. Guardar `HIGHESTMODSEQ` por folder; en
  cada sync, `FETCH 1:* (FLAGS) (CHANGEDSINCE <modseq>)` trae **solo lo que cambió**. Sin
  CONDSTORE habría que traer los flags de todo el folder cada vez — inviable con 50.000 mensajes.
  Delta Chat llegó a la misma conclusión: CONDSTORE es lo que hace viable el sync de estado `Seen`
  entre dispositivos.
- **QRESYNC** para los borrados: `VANISHED (EARLIER)` da la lista de UIDs expurgados desde un
  modseq. Delta Chat lo saltea porque no le importan los expunges; **a nosotros sí nos importan**
  (un mail borrado desde el celular debe desaparecer de la PWA). Dovecot soporta QRESYNC bien.
- **De nosotros hacia IMAP:** el patrón local/remote de Mailspring. Cambio local inmediato en la DB
  (UI optimista), operación remota encolada en tabla `tasks`, reintentable con backoff.
- **Conflictos:** el servidor IMAP gana siempre. Somos un cache, no la fuente de verdad. Si una
  tarea remota falla porque el mensaje ya no existe (`NO [TRYCREATE]` / UID desaparecido), se
  descarta la tarea y se resincroniza el folder. **Nunca** reintentar infinitamente.

### 4.7 Reconexión, backoff y el backfill inicial

- **Backoff exponencial con jitter** (base 1s, cap 5min). Sin jitter, N buzones que pierden
  conexión por un restart de Mailcow reconectan sincronizados y producen un thundering herd contra
  el propio Dovecot.
- **Circuit breaker por cuenta:** tras M fallos consecutivos de auth, pausar la cuenta y alertar.
  Reintentar credenciales inválidas contra Dovecot indefinidamente puede disparar fail2ban y
  bloquear la IP del propio sync engine — un auto-DoS perfectamente evitable.
- **IDLE muere en silencio.** Delta Chat redujo su timeout de IDLE y lo resetea con cada keepalive
  `OK Still here`. Adoptar: re-emitir IDLE cada ~10 min (no los 29 del RFC), y mantener un poll de
  respaldo (`STATUS` cada 5 min) que detecte divergencias aunque IDLE parezca vivo.
- **Backfill inicial** (la lección de Nylas — "initial sync can take quite a while"): sync por
  fases con la UI usable desde el minuto uno.
  1. `LIST` + `STATUS` de todos los folders → estructura visible.
  2. Headers de los últimos 30 días de INBOX → **la PWA ya es usable**.
  3. Bodies de esos 30 días.
  4. Headers del resto de INBOX, luego el resto de folders (más recientes primero).
  5. Bodies dentro de la ventana de cache.
  6. Texto para FTS del histórico completo, a baja prioridad.
  Cada fase con checkpoint persistido para poder reanudar tras un restart.

---

## 5. Tiempo real: de IMAP IDLE al browser

### 5.1 El costo real de IDLE

El dato duro: **IDLE necesita un socket persistente por mailbox**, porque está scopeado a un solo
folder de una sola conexión autenticada. **No hay multiplexing en el protocolo.** 10.000 usuarios
mirando su inbox = 10.000 sockets TCP long-lived, cada uno autenticado, con SELECT hecho, idling y
con su propio timer de 29 minutos.

**Nuestra escala:** ~100 buzones. Si hiciéramos IDLE sobre 4 folders por buzón (INBOX, Sent,
Drafts, Archive) serían 400 conexiones persistentes contra Dovecot. Es mucho — Dovecot lo
soporta, pero hay que subir `mail_max_userip_connections` y los límites de proceso, y estaríamos
consumiendo recursos del servidor de correo productivo.

### 5.2 Diseño recomendado: IDLE selectivo + NOTIFY donde se pueda

1. **IDLE solo sobre INBOX**, y **solo para cuentas con un cliente activo** (sesión PWA abierta o
   con actividad en los últimos ~15 min). Cuentas inactivas: poll con `STATUS` cada 5-15 min. Esto
   lleva las 400 conexiones a típicamente 5-20 en un día laboral normal.
2. **Evaluar IMAP NOTIFY (RFC 5465)** — Dovecot lo soporta y go-imap v2 lo tiene en la lista de
   extensiones. NOTIFY permite recibir eventos de **múltiples mailboxes sobre una sola conexión**,
   que es exactamente la limitación de IDLE. **Es la solución correcta al problema de fan-out y
   debe validarse empíricamente contra nuestro Dovecot en el spike técnico.** Si funciona,
   colapsamos N conexiones por usuario a 1.
3. **Pool de conexiones separado** para operaciones on-demand (fetch de body, búsqueda server-side,
   tareas remotas), con límite duro por cuenta. Nunca reutilizar la conexión de IDLE para trabajo
   (habría que salir de IDLE, perdiendo eventos).

### 5.3 Push al browser: **SSE, no WebSocket**

| Criterio | SSE | WebSocket |
|---|---|---|
| Dirección | Server→client (nos alcanza: el cliente escribe por REST) | Full-duplex (no lo necesitamos) |
| Reconexión | **Automática y nativa**, con `Last-Event-ID` para resume sin pérdida | Hay que reimplementarla a mano |
| Infra | HTTP estándar, **sin upgrade de protocolo**; pasa por Traefik/proxies/CDN sin config especial | Requiere configuración de upgrade en el reverse proxy |
| Debug | Curl-eable | Herramientas específicas |
| HTTP/2 | Compatible (y multiplexado, lo que elimina el viejo límite de 6 conexiones) | — |

El consenso 2026 es explícito: SSE cuando el cliente solo escucha (notificaciones, dashboards,
streams de eventos); WebSocket cuando el cliente también empuja con frecuencia (chat, colaborativo).
**Un webmail es el primer caso**: las acciones del usuario (marcar leído, mover, enviar) son
operaciones REST con semántica de request/response y necesitan respuesta de error — no encajan en
un stream. El `Last-Event-ID` es particularmente valioso: es exactamente la primitiva de "resume
donde quedé" que necesita una PWA en un celular que entra y sale de cobertura.

**Diseño:** un endpoint `GET /events` (SSE) por sesión, que multiplexa los eventos de todos los
folders del buzón. Formato de evento inspirado en JMAP: `{type: "state", mailbox: "...",
newState: "..."}` — el cliente recibe el aviso de "hay cambios" y pide el delta por
`GET /changes?since=<state>`. **Nunca mandar el contenido del mail por SSE**: eventos chicos,
el cliente busca lo que necesita. Esto mantiene el stream barato y hace que perder eventos sea
recuperable.

Fuentes: <https://cli.nylas.com/guides/imap-idle-explained>, <https://en.wikipedia.org/wiki/IMAP_IDLE>,
<https://ably.com/blog/websockets-vs-sse>,
<https://www.nimbleway.com/blog/server-sent-events-vs-websockets-what-is-the-difference-2026-guide>

---

## 6. (a) Recomendación de stack

| Capa | Elección | Razón principal |
|---|---|---|
| **Lenguaje backend** | **Go 1.23+** | `go-imap/v2` es la única librería cliente IMAP open source con QRESYNC+CONDSTORE+IDLE+NOTIFY y API limpia. El perfil (N conexiones persistentes + parsing concurrente + API HTTP) es el caso canónico de Go. |
| **Cliente IMAP** | `emersion/go-imap/v2` **vendorizado y encapsulado** | Cobertura de extensiones inigualada. Riesgo: beta desde feb-2025. Mitigación obligatoria en §2.1. |
| **Parsing MIME** | `emersion/go-message` (primario, streaming) + `jhillyerd/enmime` (fallback) | Memoria acotada en mails grandes; dos parsers fallan en conjuntos distintos de mails rotos. |
| **SMTP / SASL** | `emersion/go-smtp`, `emersion/go-sasl` | Maduros, mismo ecosistema. |
| **Storage metadata** | **PostgreSQL 17** | Ya operado por el grupo. Transaccionalidad para el patrón local/remote tasks. JSONB + columnas generadas. |
| **Storage blobs** | **Filesystem content-addressed** (sha256), refcounted | Dedupe global, backup incremental trivial, DB liviana. |
| **Índice FTS** | **`tsvector` (GIN) + `pg_trgm`** para MVP; **Meilisearch** en fase 2 | Cero infra nueva. Puerta de salida garantizada por la interfaz `Indexer` + comando `reindex`. |
| **Push al browser** | **SSE** | Reconexión y resume nativos; sin config de proxy; el cliente escribe por REST. |
| **API** | **REST/JSON propia, con semántica inspirada en JMAP** | No existe servidor JMAP usable en Go. JMAP como *inspiración* deja la puerta abierta sin el costo de la spec. |
| **Frontend** | React + TypeScript (PWA) | Confirmado del brief. Sin objeciones del research. |

**Anti-recomendaciones explícitas:** no SQLite (multi-buzón concurrente); no bleve (no aporta sobre
Postgres ni compite con Meilisearch); no JMAP server-side propio en el MVP; no Rust (falta la
librería IMAP equivalente); no Node (perfil de carga equivocado).

---

## 7. (b) Esquema de datos preliminar

Principios: **el `message_id` interno nunca deriva del UID IMAP** (lección mbsync §1.4); **blobs
fuera de la DB**; **estado de sync persistido como hecho independiente** (lección Delta Chat §1.3);
**el índice es derivable y reconstruible** (lección Thunderbird §1.6).

```sql
-- ══════════ Cuentas y folders ══════════

accounts
  id              uuid PK
  email           text UNIQUE NOT NULL
  imap_host       text NOT NULL
  imap_user       text NOT NULL
  credential_enc  bytea NOT NULL      -- AES-256-GCM, master key fuera de la DB
  credential_kid  text NOT NULL       -- key id, para rotación
  status          text NOT NULL       -- active | paused | auth_failed
  backfill_phase  smallint NOT NULL   -- 0..6, checkpoint del sync inicial (§4.7)
  created_at, updated_at timestamptz

folders
  id              uuid PK
  account_id      uuid FK → accounts
  name            text NOT NULL       -- nombre IMAP crudo (modified UTF-7 decodificado)
  path            text NOT NULL       -- normalizado con delimitador
  role            text                -- inbox|sent|drafts|trash|junk|archive|null (SPECIAL-USE)
  uidvalidity     bigint              -- ⚠ si cambia → invalidar mapeo de UIDs, NO borrar mensajes
  uidnext         bigint              -- persistido SIEMPRE, aunque el FETCH venga vacío
  highestmodseq   bigint              -- CONDSTORE/QRESYNC
  last_synced_at  timestamptz
  sync_state      text                -- idle|syncing|error
  UNIQUE (account_id, path)

-- ══════════ Mensajes ══════════

messages
  id              uuid PK             -- ⚠ NUESTRO id, estable, independiente del UID IMAP
  account_id      uuid FK
  rfc822_msgid    text                -- Message-ID del header (puede faltar o duplicarse)
  blob_sha256     char(64)            -- → blobs; NULL si aún no se descargó el raw
  thread_id       uuid FK → threads
  in_reply_to     text
  references_hdr  text[]              -- para JWZ
  subject         text
  subject_norm    text                -- normalizado (strip Re:/Fwd:/RV:/AW:) para fallback JWZ
  from_addr       text
  from_name       text
  to_addrs        jsonb
  cc_addrs        jsonb
  bcc_addrs       jsonb
  date_sent       timestamptz
  date_received   timestamptz
  size_bytes      bigint
  has_attachments boolean
  mime_structure  jsonb               -- árbol de parts sin contenido (para fetch selectivo)
  parse_status    text                -- ok | partial | failed  (§4.2: failed nunca frena el sync)
  charset_guessed boolean
  body_cached     boolean             -- ¿está en la ventana de cache? (§3.1)
  created_at, updated_at

  INDEX (account_id, date_received DESC)
  INDEX (thread_id)
  INDEX (rfc822_msgid)

-- El mismo mensaje puede estar en N folders (Gmail-style o copias).
-- El UID vive acá, NO en messages.
message_folders
  message_id      uuid FK → messages
  folder_id       uuid FK → folders
  imap_uid        bigint NOT NULL
  uidvalidity     bigint NOT NULL     -- epoch al que pertenece este UID
  flags           text[]              -- \Seen \Answered \Flagged \Draft \Deleted + keywords
  modseq          bigint              -- CONDSTORE, por mensaje
  PRIMARY KEY (message_id, folder_id)
  UNIQUE (folder_id, uidvalidity, imap_uid)
  INDEX (folder_id, imap_uid)

-- ══════════ Cuerpos y búsqueda ══════════

message_bodies                        -- solo dentro de la ventana de cache (§3.1)
  message_id      uuid PK FK → messages
  text_plain      text
  html_sanitized  text                -- ya pasado por bluemonday server-side (§4.3)
  cached_at       timestamptz         -- para LRU

message_search                        -- SIEMPRE, para todos los mensajes
  message_id      uuid PK FK → messages
  account_id      uuid                -- desnormalizado: filtro de tenant en el índice
  tsv             tsvector            -- subject + from + to + body plano
  INDEX USING GIN (tsv)
  INDEX USING GIN (account_id)        -- o índice parcial/compuesto según plan real

threads
  id              uuid PK
  account_id      uuid FK
  subject_norm    text
  first_date      timestamptz
  last_date       timestamptz
  message_count   int
  INDEX (account_id, last_date DESC)

-- ══════════ Blobs ══════════

blobs                                 -- metadata; el contenido está en filesystem
  sha256          char(64) PK
  size_bytes      bigint
  path            text                -- blobs/ab/cd/abcd...
  refcount        int NOT NULL        -- GC de huérfanos
  encrypted       boolean DEFAULT false
  created_at

attachments
  id              uuid PK
  message_id      uuid FK
  part_id         text                -- ej "2.1", para el FETCH selectivo por IMAP
  filename        text
  mime_type       text
  size_bytes      bigint
  blob_sha256     char(64) FK → blobs  -- NULL = no descargado aún (on-demand)
  is_inline       boolean
  content_id      text                -- para cid: en el HTML

-- ══════════ Escritura y drafts ══════════

tasks                                 -- patrón local/remote de Mailspring (§1.2)
  id              uuid PK
  account_id      uuid FK
  type            text                -- set_flags|move|delete|append|send|fetch_body
  payload         jsonb
  status          text                -- pending|running|done|failed|abandoned
  attempts        int
  next_attempt_at timestamptz         -- backoff exponencial + jitter (§4.7)
  last_error      text
  created_at
  INDEX (status, next_attempt_at)

drafts                                -- entidad de primera clase, ID estable (§4.5)
  id              uuid PK
  account_id      uuid FK
  in_reply_to_msg uuid FK → messages
  subject, to_addrs, cc_addrs, bcc_addrs, body_html, body_text
  imap_uid        bigint              -- UID de la última versión persistida en IMAP (para borrarla)
  last_synced_at  timestamptz         -- para el debounce
  updated_at
```

**Notas de diseño:**
- Un cambio de `folders.uidvalidity` **no borra mensajes**: invalida el mapeo en `message_folders`
  y dispara un re-descubrimiento de UIDs por `Message-ID`. Los blobs y el índice se preservan.
  Esta es exactamente la ventaja de mbsync sobre OfflineIMAP (§1.4), y evita re-descargar un buzón
  de 100 GB por un evento administrativo del servidor.
- `message_search` se puede reconstruir enteramente desde `messages` + blobs → habilita el cambio
  de motor de búsqueda sin migración de datos (§3.5).
- `messages.id` es UUID generado por nosotros, **no** un hash de headers. Esto evita explícitamente
  el bug documentado de Mailspring de colisión de IDs (§1.2).

---

## 8. (c) Top 10 riesgos técnicos y mitigación

| # | Riesgo | Impacto | Prob. | Mitigación |
|---|---|---|---|---|
| **R1** | **`go-imap/v2` sigue en beta desde feb-2025 (~18 meses sin release).** Si el mantenimiento se detiene, quedamos sin upstream para bugs de protocolo. | Alto | Media | Vendorizar + pinnear hash desde el commit 1. **Encapsular el 100% del acceso IMAP tras interfaz propia** (`internal/imap`). Presupuestar mantener un fork. Spike temprano contra nuestro Dovecot para validar QRESYNC/NOTIFY reales antes de comprometerse. |
| **R2** | **UIDVALIDITY cambia y se dispara un re-download completo** de buzones de 10-100 GB. | Alto | Media | `message_folders` desacopla nuestro ID del UID (§7). Ante cambio de UIDVALIDITY: re-mapear por `Message-ID` + `Date` + tamaño, **no re-descargar**. Blobs y FTS se preservan. Test de integración que simule el cambio. |
| **R3** | **Identidad de mensajes ambigua**: `Message-ID` ausente o duplicado (mailing lists, mails generados por scripts). Mailspring documenta perder mensajes por colisión de IDs. | Alto | Alta | PK propia (UUID), **nunca** hash de headers. Deduplicar por `(account, sha256 del raw)`, no por `Message-ID`. `Message-ID` es un atributo indexado, no una clave. |
| **R4** | **MIME/charset patológico rompe el sync de un folder entero** (el probable origen de los "Incomplete sync" de Nylas). | Alto | Alta | `parse_status='failed'` + guardar el blob crudo + continuar **siempre**. Doble parser (go-message → enmime fallback). Suite de corpus de mails rotos desde el día 1. Métrica de tasa de fallo de parseo con alerta. |
| **R5** | **XSS por bypass del sanitizer de HTML.** Fastmail, Proton y Gmail tuvieron bypasses documentados. Un XSS acá = toda la sesión de correo comprometida. | **Crítico** | Media | 3 capas: bluemonday server-side + DOMPurify client-side + **`<iframe sandbox>` sin `allow-scripts`/`allow-same-origin` con CSP `default-src 'none'`**. La capa 3 es la que salva cuando 1 y 2 fallan. Strippear CSS no inlineado (anti "Spy Sheets"). |
| **R6** | **Endpoint de proxy de imágenes convertido en SSRF / open proxy** contra la red interna del VPS (Mailcow, Postal, Tailscale). | **Crítico** | Media | HMAC obligatorio en la URL. Rechazar IPs privadas/loopback/link-local **tras resolver DNS y tras cada redirect** (anti DNS rebinding). Timeout y límite de tamaño duros. Forzar content-type imagen. |
| **R7** | **Fan-out de conexiones IMAP persistentes** satura Dovecot o dispara fail2ban contra nuestra propia IP. | Alto | Media | IDLE solo en INBOX y solo para cuentas con sesión activa; poll para el resto. Validar **NOTIFY** para colapsar N folders en 1 conexión. Pool separado para on-demand con límite por cuenta. **Circuit breaker por cuenta tras M fallos de auth.** Ajustar `mail_max_userip_connections` en Dovecot. Backoff con jitter (anti thundering herd tras restart de Mailcow). |
| **R8** | **tsvector se degrada** al acercarnos a ~5M mensajes (100 buzones × 50k). Estamos en el borde, no cómodos. | Medio | **Alta** | Interfaz `Indexer` + comando `reindex` **desde el día 1** (requisito de diseño, no feature). Benchmark con corpus sintético de 5M antes de cerrar el MVP. Meilisearch pre-evaluado como fase 2 con path de migración escrito. |
| **R9** | **Backfill inicial arruina la UX** — la lección explícita de Nylas ("initial sync can take quite a while"). Un buzón de 100 GB puede tardar días. | Alto | Alta | Sync por fases con checkpoint persistido (§4.7): la PWA es usable tras la fase 2 (headers de 30 días de INBOX). Barra de progreso honesta por fase. Cache parcial por recencia (Mailspring: solo 3 meses de bodies) como decisión de producto, no como limitación oculta. |
| **R10** | **Divergencia silenciosa** entre nuestro cache y el estado real de Dovecot: flags o mensajes desincronizados sin que nadie se entere. IDLE muere en silencio. | Alto | Alta | El servidor IMAP **gana siempre** (somos cache, no fuente de verdad). Poll de `STATUS` de respaldo cada 5 min aunque IDLE parezca vivo. **Job de reconciliación nocturno** (deep sync tipo Mailspring) que compara UIDs y flags folder por folder. Métrica de divergencias detectadas — si sube, hay un bug en el sync incremental. |

**Riesgos honorables (fuera del top 10 pero no ignorables):** duplicación de drafts por sync
ingenuo (§4.5); merge incorrecto de hilos por fallback de subject sin ventana temporal (§4.1);
pérdida del dedupe de blobs si se activa cifrado por buzón (§3.4); custodia de contraseñas IMAP
en claro — la falla explícita de Nylas — mitigable evitando custodiarlas (master password de
Dovecot / XOAUTH2).

---

## 9. Próximos pasos sugeridos

1. **Spike técnico #1 (bloqueante, ~2 días):** validar contra nuestro Dovecot real, con
   `go-imap/v2` beta.8, que funcionan **QRESYNC (`VANISHED EARLIER`), CONDSTORE (`CHANGEDSINCE`) y
   NOTIFY multi-mailbox**. Si NOTIFY funciona, R7 se desactiva casi por completo. Si QRESYNC falla,
   hay que replantear la estrategia de detección de borrados.
2. **Spike técnico #2 (~1 día):** benchmark de `tsvector` + GIN con corpus sintético de **5M
   mensajes** en el hardware del VPS. Es la validación directa de R8 y define si Meilisearch entra
   en el MVP o en la fase 2.
3. **Corpus de tests de MIME patológico** antes de escribir el parser, no después (R4).
4. Documento de arquitectura general que compare formalmente esta **Opción B** contra la
   alternativa **"migrar a Stalwart y hacer solo la PWA contra JMAP"** (§1.7) — esa comparación
   excede el alcance de este informe pero es la decisión de mayor palanca del proyecto.

---

## 10. Fuentes

**Sync engines**
- Nylas Sync Engine: <https://github.com/nylas/sync-engine> · <https://github.com/nylas/sync-engine/blob/master/README.md>
- Mailspring-Sync: <https://github.com/Foundry376/Mailspring-Sync> · <https://github.com/Foundry376/Mailspring-Sync/blob/master/README.md>
- Mailspring DB guide: <https://foundry376.github.io/Mailspring/guides/Database.html>
- Mailspring bug "too many SQL variables": <https://github.com/Foundry376/Mailspring/issues/1951>
- Mailspring estado del proyecto: <https://biggo.com/news/202502040723_mailspring-development-concerns>
- Delta Chat / chatmail core: <https://github.com/chatmail/core> · <https://rs.delta.chat/deltachat/imap/index.html>
- DC issue UIDVALIDITY/uid_next: <https://github.com/chatmail/core/issues/2188>
- DC issue CONDSTORE: <https://github.com/chatmail/core/issues/2941>
- DC issue IDLE timeout: <https://github.com/deltachat/deltachat-core-rust/issues/5093>
- DC standards: <https://github.com/chatmail/core/blob/main/standards.md>
- mbsync vs OfflineIMAP: <https://anarc.at/blog/2021-11-21-mbsync-vs-offlineimap/> · <https://vxlabs.com/2019/07/05/mbsync-vs-offlineimap-speed/>
- OfflineIMAP UIDVALIDITY issue: <https://github.com/OfflineIMAP/offlineimap/issues/190>
- State files compat: <https://www.mail-archive.com/isync-devel@lists.sourceforge.net/msg01778.html>
- notmuch: <https://notmuchmail.org/> · lieer: <https://github.com/gauteh/lieer>
- Unix mail tools: <https://www.kmjn.org/notes/unix_style_mail_tools.html>
- Stalwart: <https://stalw.art/architecture/> · <https://github.com/stalwartlabs/stalwart>

**Librerías Go**
- go-imap releases: <https://github.com/emersion/go-imap/releases>
- go-imap v2 imapclient: <https://pkg.go.dev/github.com/emersion/go-imap/v2/imapclient>
- go-imap capabilities: <https://github.com/emersion/go-imap/blob/v2/capability.go>
- go-message: <https://github.com/emersion/go-message> · charset: <https://github.com/emersion/go-message/blob/master/charset/charset.go>
- enmime: <https://github.com/jhillyerd/enmime>
- go-jmap (foxcpp): <https://github.com/foxcpp/go-jmap> · (rockorager): <https://sr.ht/~rockorager/go-jmap/>
- JMAP implementations: <https://jmap.io/software/>

**Storage e indexación**
- Postgres FTS vs alternativas: <https://supabase.com/blog/postgres-full-text-search-vs-the-rest>
- SQLite vs PostgreSQL: <https://devops-daily.com/comparisons/sqlite-vs-postgresql>
- Guía text search: <https://synehq.com/blog/the-complete-guide-to-text-search-postgresql-duckdb-and-beyond>

**Problemas duros**
- JWZ threading: <https://www.jwz.org/doc/threading.html> · <https://mboxviewer.net/glossary/threading/> · <https://www.bowaggoner.com/bomail/writeups/threads.html>
- Fastmail image proxying: <https://www.fastmail.com/blog/better-security-and-privacy-through-image-proxying/>
- Fastmail DOM clobbering: <https://www.fastmail.com/blog/sanitising-html-the-dom-clobbering-issue/>
- Gmail image proxy: <https://words.filippo.io/how-the-new-gmail-image-proxy-works-and-what-this-means-for-you/>
- CSS attacks: <https://portswigger.net/research/css-the-bomb-inside-your-inbox> · <https://roots.ec/blog/spy-sheets/>
- Proton Mail vulns: <https://www.sonarsource.com/blog/code-vulnerabilities-leak-emails-in-proton-mail/>
- Rendering untrusted HTML: <https://making.close.com/posts/rendering-untrusted-html-email-safely/>

**Tiempo real**
- IMAP IDLE explicado: <https://cli.nylas.com/guides/imap-idle-explained> · <https://en.wikipedia.org/wiki/IMAP_IDLE>
- SSE vs WebSockets: <https://ably.com/blog/websockets-vs-sse> · <https://www.nimbleway.com/blog/server-sent-events-vs-websockets-what-is-the-difference-2026-guide>
