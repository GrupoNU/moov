# 01 — Panorama competitivo: webmails open source (agosto 2026)

> **Proyecto:** Webmail open source de clase mundial para Mailcow
> **Arquitectura objetivo:** "Opción B" — sync engine backend (IMAP/Dovecot → base propia + índice full-text) + API moderna (probablemente JMAP) + frontend PWA React/TS
> **Fecha del research:** 2026-08-07
> **Naturaleza:** 100% documental (web + GitHub). No se tocó producción.

---

## 0. Resumen de la tesis

El research confirma que **el hueco que apuntamos existe y está sorprendentemente vacío**. El panorama se parte en dos mundos que no se tocan:

1. **El mundo IMAP-directo (Roundcube, SnappyMail, SOGo, Cypht, alps):** maduro, estable, compatible con Dovecot/Mailcow — pero arquitectónicamente incapaz de dar la experiencia "Gmail/Fastmail" porque cada acción del usuario es un round-trip IMAP sincrónico. No hay estado propio, no hay índice propio, no hay push real.

2. **El mundo JMAP-moderno (Bulwark, root-fr/jmap-webmail, Twake Mail):** UX genuinamente 2026, stack React/TS moderno, arquitectura correcta — pero **todos exigen un servidor que hable JMAP nativo** (Stalwart o Apache James). **Ninguno funciona contra Dovecot.**

El puente entre ambos mundos —un backend que hable IMAP hacia abajo y JMAP moderno hacia arriba, con base propia e índice full-text— es exactamente la Opción B, y **no existe hoy como producto vivo**. La única implementación conocida de ese puente es el `jmap-proxy` de Fastmail (Perl, uso interno/histórico) y un `jmap-proxy-python` experimental. Ver §2.5 y §5.

---

## 1. Clásicos IMAP-directo

### 1.1 Roundcube

| Item | Dato |
|------|------|
| **Arquitectura** | IMAP-directo, stateless por request. MySQL/Postgres solo para contactos, settings y cache liviano. |
| **Stack** | PHP 8.x + JavaScript vanilla/jQuery. Skin **Elastic** (LESS) por defecto desde 1.6. |
| **Licencia** | GPL-3.0 (con excepción para skins/plugins) |
| **Mantenimiento** | **Activo pero peligrosamente concentrado.** Commits diarios; último visto **2026-08-05**. La abrumadora mayoría de los commits son de **Aleksander Machniak (alec@alec.pl)** — un solo desarrollador principal. |
| **Ownership** | **Adquirido por Nextcloud (nov. 2023).** El fundador Thomas Brüderli se retiró. Nextcloud declaró explícitamente que **NO habrá merge** con Nextcloud Mail: son productos separados para patrones de deployment distintos. |
| **Versión actual** | 1.6.15 (2026-03-29, LTS) + **1.7 RC** — la primera feature release en años, tras un feature freeze largo. 1.7 trae filtros de sintaxis de búsqueda, composición en Markdown, OAuth2 mejorado, PHP 8.4. |

**Qué hace bien:** es el estándar de facto. Compatibilidad IMAP brutal, ecosistema de plugins enorme, i18n masiva, seguridad tomada en serio (CVEs parchean rápido en 1.5/1.6 LTS), y Elastic es responsive y decente.

**Por qué NO llena el hueco:**
- **El modelo de datos es el problema, no el skin.** Cada carga de lista de mensajes es un `FETCH` IMAP en vivo. Sin base propia, la búsqueda es `IMAP SEARCH` delegada al servidor: en Dovecot sin Solr/FTS eso es un scan lineal, lento y sin ranking. No hay forma de hacer "instant search as-you-type" sobre esta arquitectura.
- **Sin push real.** El navegador poletea; no hay `IDLE` propagado al cliente de forma eficiente.
- **Sin offline.** No es una PWA con store local; es una app web clásica server-rendered.
- **Threading débil.** Depende de `IMAP THREAD` del servidor, sin conversación persistida ni cross-folder.
- **Riesgo de bus factor 1.** El proyecto vive de un desarrollador. La historia de **Roundcube Next** (§6) es la advertencia.

**Nota histórica crítica — "Roundcube Next":** en 2015 Roundcube + Kolab Systems lanzaron un crowdfunding para reescribir la app entera. Recaudaron **US$103.541** (meta: $80k). **Kolab y Roundcube detuvieron el desarrollo en 2016**; los backers no recibieron ni producto ni updates ni reembolsos. Es el caso testigo #1 de §6.

**Fuentes:**
- https://github.com/roundcube/roundcubemail
- https://en.wikipedia.org/wiki/Roundcube
- https://nextcloud.com/blog/open-source-email-pioneer-roundcube-comes-aboard-nextcloud/
- https://nextcloud.com/blog/roundcubes-future-at-nextcloud-an-interview-with-the-founders/
- https://lwn.net/Articles/953228/
- https://github.com/roundcube/roundcubemail/blob/master/skins/elastic/README.md

---

### 1.2 SnappyMail

| Item | Dato |
|------|------|
| **Arquitectura** | IMAP-directo, **"no database required"**. Contactos opcionalmente en MySQL/MariaDB. |
| **Stack** | PHP 7.4+, JavaScript ES2020, **Knockout.js 3.5.1**, Rollup, Gulp/Yarn. Editor Squire modificado, OpenPGP.js v5. |
| **Licencia** | AGPL-3.0 |
| **Mantenimiento** | Activo. ~7.184 commits en master. ~1.7k stars, 212 forks. **Un solo mantenedor: @the-djmaze.** |
| **Origen** | Fork de **RainLoop** (que quedó semi-abandonado y con CVEs sin parchear). |

**Qué hace bien:** es el webmail IMAP-directo **más rápido y liviano** que existe. Bundle JS ~66% menor que RainLoop (361 KB min+gzip vs 1.2 MB). Privacidad seria (eliminó Gravatar, integraciones sociales, tracking). Editor de Sieve avanzado. PGP con generación de claves ECDSA/EDDSA. Dark mode. Service worker para notificaciones.

**Por qué NO llena el hueco:**
- Misma limitación arquitectónica que Roundcube: **sin store propio, sin índice propio**. La velocidad percibida viene de un frontend liviano, no de un modelo de datos mejor. Con un buzón de 50k mensajes vuelve a depender del IMAP del servidor.
- **Knockout.js** es un framework de 2011 en modo mantenimiento. Cualquier contribución moderna choca contra ese muro.
- **Bus factor 1** explícito y reconocido.
- Sin offline real, sin snooze, sin undo send, sin conversación persistida.

**Fuentes:**
- https://github.com/the-djmaze/snappymail

---

### 1.3 SOGo — el incumbente en Mailcow

| Item | Dato |
|------|------|
| **Arquitectura** | IMAP-directo + backend groupware propio (CalDAV/CardDAV en Postgres/MySQL). Frontend AngularJS 1.x + Material Design. |
| **Stack** | **Objective-C sobre GNUstep** (!) + AngularJS 1.x. |
| **Licencia** | GPL-2.0 / LGPL-2.1 |
| **Mantenimiento** | Activo (Inverse/Alinto), pero release cadence lenta y base tecnológica congelada. |
| **Rol en Mailcow** | Webmail por defecto + CalDAV + CardDAV + **ActiveSync (EAS)** vía paquete separado. |

**Qué hace bien —y hay que reconocerlo, porque es la razón por la que sigue ahí:**
- **Groupware completo sin piezas extra.** CalDAV/CardDAV que "just works" en iOS y Thunderbird. Esto es un ancla real: cualquier reemplazo que rompa calendario/contactos es un downgrade neto para el usuario.
- **ActiveSync**, que casi ningún open source tiene.

**Limitaciones concretas reportadas por usuarios de Mailcow** (esto es lo que valida nuestro proyecto):

1. **Lentitud con buzones medianos — el issue central.** [mailcow-dockerized#4011](https://github.com/mailcow/mailcow-dockerized/issues/4011): la interfaz se vuelve "very slow" al scrollear un inbox de **~3.000 mensajes**, con lag notorio tras 20-30 mails. Lo brutal del caso: el reporter corría **24 cores / 16 GB RAM / SSD** con una instalación fresca de 5 usuarios y un solo buzón con mails. **No es un problema de hardware — es arquitectónico.** El issue está etiquetado `help-wanted` + `upstream`, es decir: Mailcow lo reconoce pero no puede arreglarlo porque es de SOGo. Nuestro caso Crash (89 buzones, 584 GB) está uno o dos órdenes de magnitud por encima de ese umbral de dolor.
2. **Lentitud generalizada** reportada de forma recurrente desde al menos feb. 2023 en el foro de la comunidad ("Very Slow SOGO mail").
3. **Bugs de ciclo de vida de cuentas:** un buzón creado como "inactive" o "disallow login" **queda inutilizable en SOGo incluso después de activarlo** ([#7136](https://github.com/mailcow/mailcow-dockerized/issues/7136)).
4. **Fricción de autenticación:** buzones nuevos que aleatoriamente no dan acceso a webmail; usuarios rebotados al UI de Mailcow tras updates ([#6392](https://github.com/mailcow/mailcow-dockerized/issues/6392), [#5225](https://github.com/mailcow/mailcow-dockerized/issues/5225)); problemas cuando el auto-redirect nunca se activó ([#6442](https://github.com/mailcow/mailcow-dockerized/issues/6442)).
5. **Incompatibilidad con WebAuthn** en ciertas versiones.

**Por qué NO llena el hueco:** además de la performance, el stack **Objective-C/GNUstep + AngularJS 1.x** es un callejón sin salida. AngularJS está EOL desde 2022. La barrera de entrada para contribuir (Objective-C/GNUstep en 2026) es tan alta que el pool de contributors es diminuto. La UX es "Material Design 2015" y no va a cambiar.

**Implicancia para nuestro diseño:** SOGo no se apaga — se **complementa**. Nuestro webmail debe convivir con SOGo dejando CalDAV/CardDAV/ActiveSync en su lugar, al menos en MVP y post-MVP temprano. Matar SOGo sin resolver calendario/contactos/EAS es regresivo.

**Fuentes:**
- https://github.com/mailcow/mailcow-dockerized/issues/4011
- https://community.mailcow.email/d/2189-very-slow-sogo-mail
- https://github.com/mailcow/mailcow-dockerized/issues/7136
- https://github.com/mailcow/mailcow-dockerized/issues/6442
- https://github.com/mailcow/mailcow-dockerized/issues/6392
- https://github.com/mailcow/mailcow-dockerized/issues/5225
- https://en.wikipedia.org/wiki/SOGo
- https://www.sogo.nu/support/faq/dedicated-separate-sogo-instance-for-activesync.html

---

### 1.4 Cypht

| Item | Dato |
|------|------|
| **Arquitectura** | **Agregador**, no cliente de un buzón. Conecta directo a IMAP/SMTP, **JMAP** y **EWS (Exchange)**. Sin cache propio significativo. |
| **Stack** | PHP + JavaScript. Todo construido como "module sets" (plugins). |
| **Licencia** | LGPL-2.1 |
| **Mantenimiento** | **Activo.** ~1.671 stars, 228 forks, ~7.515 commits, 139 issues abiertos. Reuniones mensuales de comunidad, Gitter. Fundador: Jason Munro. Último push visto: 2026-08-07. |

**Qué hace bien:** su propuesta es distinta y honesta — "como un lector de noticias, pero para e-mail". Unifica múltiples cuentas + feeds RSS/Atom en una vista. Arquitectura modular real. **Es de los pocos open source con soporte JMAP y EWS.**

**Por qué NO llena el hueco:** el objetivo es **agregación multi-cuenta liviana**, no ser el cliente primario de clase mundial de un buzón grande. No tiene sync engine ni índice propio, así que hereda la performance del servidor upstream. La UX es funcional/densa, no pulida. El soporte JMAP y EWS son issues aún en evolución (#180, #247).

**Fuentes:**
- https://github.com/cypht-org/cypht

---

### 1.5 alps

| Item | Dato |
|------|------|
| **Arquitectura** | **Stateless por diseño** — es su bandera. Sin base de datos, sin cache. IMAP en vivo. |
| **Stack** | **Go**. Templates server-side. Plugins en Lua o Go. |
| **Licencia** | MIT |
| **Mantenimiento** | **Activo.** Movido de `~emersion` a `~migadu` en SourceHut; commits recientes (jul. 2026). **Financiado por NLnet / NGI0 Commons Fund desde abr. 2025.** Corre en producción en `webmail.migadu.com` con uptime ~100%. |

**Qué hace bien:** minimalismo ejemplar. Rápido, chico, auditable, multi-tenant, CalDAV/CardDAV, theming responsive, sistema de plugins. Tiene financiamiento público (el modelo de sostenibilidad más sano de la lista).

**Por qué NO llena el hueco:** **es lo opuesto exacto a nuestra tesis.** alps declara el stateless como virtud; nosotros afirmamos que el stateless es precisamente el techo que impide la experiencia de clase mundial. alps nunca va a tener instant search, offline, snooze ni undo send, porque eso requiere estado. Es un competidor filosófico, no funcional. Sirve como referencia de simplicidad operativa y de modelo de financiamiento.

**Fuentes:**
- https://sr.ht/~migadu/alps/
- https://github.com/migadu/alps
- https://nlnet.nl/project/Alps/

---

## 2. Modernos / JMAP

### 2.1 Bulwark (bulwarkmail/webmail) — el competidor más serio

| Item | Dato |
|------|------|
| **Arquitectura** | **Stateless JMAP proxy.** No mantiene base propia. Delega threading, búsqueda y estado a Stalwart. Credenciales en cookies httpOnly cifradas AES-256-GCM (30 días). Settings cifrados at-rest. |
| **Stack** | **Next.js 16 (App Router) + React 19 + TypeScript + Tailwind v4 + Zustand.** Cliente JMAP propio (RFC 8620). Tiptap (editor), next-intl, Lucide. Tests: Vitest + Playwright. |
| **Licencia** | AGPL-3.0 |
| **Mantenimiento** | **Muy activo.** ~954 stars, 136 forks. v1.8.1. **32 releases con cadencia de ~1 cada 2 días.** Nació ~jun. 2026 — proyecto joven y en explosión. |

**Qué hace bien —es genuinamente bueno y hay que estudiarlo:**
- **Suite completa:** Mail + Calendar + Contacts + **Files** (JMAP FileNode de Stalwart) en un solo cliente.
- Threading server-side ("conversaciones cosidas en Stalwart, no re-ensambladas por render en el browser").
- **Batching JMAP:** mark-read + move + fetch-next en **una sola llamada**. Esto es exactamente la ganancia de latencia que buscamos.
- **Push real:** el servidor notifica cambios de estado, no hay polling.
- **23+ keyboard shortcuts** (`j`/`k`, `c` compose), full-text search, Sieve filters, S/MIME, templates.
- PWA instalable con soporte offline, web push vía relay hosteado.
- Multi-account con vista "All accounts" cross-account.
- **24 idiomas.** Sistema de plugins + themes con marketplace propio (extensions.bulwarkmail.org). Branding configurable y overrides por dominio (interesante para nosotros como agencia multi-cliente).
- OAuth2/OIDC con auto-discovery. Setup wizard web.

**Por qué NO llena el hueco (el punto decisivo):**
- **Requiere Stalwart. No funciona con Dovecot.** Es explícito y por diseño: eligieron Stalwart porque es "un mail server en Rust con JMAP nativo — no IMAP/SMTP con JMAP atornillado". Muchas features (cambio de password, Sieve) están acopladas a APIs de Stalwart. Otros servidores JMAP "son posibles" pero requieren configurar CORS y pierden funcionalidad.
- **Para usarlo tendríamos que migrar Mailcow entero a Stalwart** — es decir, tirar Dovecot, Postfix, Rspamd, SOGo y toda la operación probada, incluyendo 584 GB de backup de Crash y 6 dominios productivos. Eso no es adoptar un webmail, es reemplazar la infraestructura.
- **Es stateless proxy**, no sync engine. Su velocidad es prestada: viene de que Stalwart hace el trabajo pesado (índice FTS, threading, push). Contra un servidor sin eso, Bulwark no rinde. **Esto valida nuestra Opción B por contraposición: si el servidor no te da el motor, tenés que construirlo vos.**
- Proyecto muy joven (nacido jun. 2026). Cadencia de release de 1 cada 2 días es señal de energía pero también de inmadurez/churn.

**Conclusión estratégica:** Bulwark es nuestro **benchmark de UX y nuestro mapa de features**, no nuestro competidor directo. Ellos resolvieron "webmail moderno para Stalwart"; el hueco abierto es "webmail moderno para Dovecot/Mailcow" — el parque instalado inmensamente mayor.

**Fuentes:**
- https://github.com/bulwarkmail/webmail
- https://bulwarkmail.org/
- https://extensions.bulwarkmail.org/
- https://alternativeto.net/software/bulwark/about/
- https://devopspack.com/stalwart-bulwark-self-hosted-email-jmap/

---

### 2.2 root-fr/jmap-webmail

| Item | Dato |
|------|------|
| **Arquitectura** | Cliente JMAP **stateless**, sin base propia. |
| **Stack** | Next.js 16 + TypeScript + Tailwind v4 + Zustand + cliente JMAP propio (RFC 8620) + next-intl. **Casi idéntico a Bulwark.** |
| **Licencia** | **MIT** (más permisiva que Bulwark) |
| **Mantenimiento** | 215 stars, 35 forks, **solo ~72 commits en main**, 4 issues abiertos. Creado dic. 2025, activo a ago. 2026. |

**Qué hace bien:** interfaz de 3 paneles desktop/mobile, calendario (RFC 8984), address book vCard, templates con variables, push en tiempo real, 8 idiomas, indicadores SPF/DKIM/DMARC en el mensaje (buen detalle de confianza), bloqueo de contenido externo, TOTP 2FA, Docker con config en runtime. **Dice soportar cualquier servidor JMAP**, no solo Stalwart (más agnóstico que Bulwark).

**Por qué NO llena el hueco:** mismo problema raíz — necesita un servidor JMAP, que Dovecot no es. Además es notablemente más chico que Bulwark (72 commits vs. 32 releases): menos maduro, un solo autor aparente. Sin sync engine ni índice propio.

**Valor para nosotros:** por ser **MIT**, su cliente JMAP en TypeScript es la referencia de implementación más reutilizable legalmente si queremos partir de código existente para el lado cliente del protocolo.

**Fuentes:**
- https://github.com/root-fr/jmap-webmail

---

### 2.3 thundersquared/jmap-webmail y otros JMAP menores

La búsqueda por `jmap-webmail in:name` devuelve **solo 7 repos en todo GitHub**, y el ecosistema entero es diminuto:

| Repo | Stars | Stack | Estado |
|------|-------|-------|--------|
| `root-fr/jmap-webmail` | 215 | Next.js/TS | Activo (§2.2) |
| `jmapio/jmap-demo-webmail` | 126 | JavaScript (Overture) | **Demo oficial de JMAP.io, prácticamente congelado desde 2015.** Es el proof-of-concept de Fastmail, no un producto. |
| `ludovicm67/jmap-webmail` | 17 | TypeScript | Experimento personal, actividad esporádica |
| `muddyland/jmap-webmail` | 1 | Svelte | "Minimalist webmail for JMAP (Stalwart)", creado may. 2026, ~sin tracción |
| `sunilshahid/Jmap-Webmail-Client` | 0 | TypeScript | Abandonado de hecho |
| `timmydo/rust-jmap-webmail` | 0 | Rust | Experimento, último toque ene. 2026 |

**No se encontró un repo `thundersquared/jmap-webmail` activo o relevante** en la búsqueda de agosto 2026 — si existió, no tiene presencia detectable hoy.

**Lectura del dato:** el "long tail" de clientes JMAP es una nube de proyectos de una persona con 0-20 stars. Salvo Bulwark, **nadie logró masa crítica**. Esto es a la vez oportunidad (el campo está abierto) y advertencia (§6: JMAP solo no genera adopción — hace falta que funcione contra el servidor que la gente ya tiene).

**Fuentes:**
- https://github.com/jmapio/jmap-demo-webmail
- https://github.com/ludovicm67/jmap-webmail
- https://github.com/muddyland/jmap-webmail

---

### 2.4 Twake Mail (Linagora)

| Item | Dato |
|------|------|
| **Arquitectura** | Cliente JMAP sobre **Apache James** (servidor JMAP nativo). Cross-platform: web + Android + iOS (Flutter/Dart). |
| **Licencia** | Open source (AGPL en su mayoría), parte de la suite Twake Workplace |
| **Mantenimiento** | **Activo y con respaldo corporativo real.** App actualizada 2026-07-08. |
| **Respaldo** | **Linagora** — el actor open source más comprometido con JMAP. Emplea al presidente de Apache James, 3 miembros del board, 5 committers y 10 contributors. Contribuyó la primera implementación JMAP open source real (2020, tras la publicación de la spec). |

**Qué hace bien:** es el único cliente JMAP con **apps móviles nativas** y una empresa detrás con modelo de negocio (soberanía digital europea / GDPR, servidores en Francia). Claim de eficiencia: "30x más eficiente en energía que los protocolos tradicionales" (marketing, pero apunta a la ventaja real de JMAP: menos round-trips).

**Por qué NO llena el hueco:** está **acoplado a Apache James**, no a Dovecot. Es una pieza de una suite (Twake Workplace) más que un webmail standalone adoptable. La orientación es enterprise/soberanía europea, no self-hosters de Mailcow. Adoptarlo implicaría, otra vez, cambiar el servidor de correo.

**Valor para nosotros:** Linagora es la mejor referencia de **cómo se ve JMAP hecho en serio y sostenido en el tiempo** — y la prueba de que el modelo que sostiene un cliente JMAP es "empresa con contratos", no "proyecto comunitario".

**Fuentes:**
- https://linagora.com/en/twake-mail
- https://linagora.com/en/why-linagora-chose-jmap-e-mail-20
- https://linagora.com/en/apache-james
- https://twake-mail.com/

---

### 2.5 ⚠️ El hallazgo crítico: JMAP sobre Dovecot no existe

Este es **el dato más importante de todo el research** para nuestra decisión de arquitectura.

- **Dovecot no soporta JMAP y no piensa hacerlo.** Los desarrolladores de Dovecot declararon explícitamente que **no tienen planes de trabajar en JMAP**, aunque "nada impide contribuciones externas". **No hay soporte JMAP en ninguna versión de Dovecot.**
- **Cyrus** era considerado más probable de tener implementación completa (estaba WIP), pero eso no ayuda a Mailcow.
- **La implementación de referencia del puente es el JMAP Proxy de Fastmail** (Perl): se pone delante de IMAP/CalDAV/CardDAV y produce JMAP. Cubre la spec completa, pero es una pieza histórica/interna de Fastmail, no un producto mantenido para terceros.
- Existe **`filiphanes/jmap-proxy-python`**: JMAP proxy a IMAP en Python async, que **funciona con Dovecot IMAP** — pero el propio repo advierte "in development".
- **Stalwart** (Rust, AGPL-3.0, ~12.700 stars) es el que resolvió esto por la vía de reemplazar todo el stack: SMTP + IMAP4rev2 + JMAP + POP3 + CalDAV/CardDAV/WebDAV en un binario de ~100 MB de RAM.

**Consecuencia directa para la Opción B:**

Si queremos JMAP contra Dovecot, **hay que construir el servidor JMAP nosotros** — no hay componente listo y confiable para meter en medio. Eso convierte nuestro sync engine en la pieza de valor central y diferencial del proyecto, no en un detalle de implementación. Y valida la decisión de tener base propia: si igual hay que traducir IMAP↔JMAP, traducir *sobre un store propio indexado* cuesta marginalmente más y da instant search, offline y push, que traducir en vivo no da.

**Riesgo a registrar:** implementar JMAP (RFC 8620 + 8621) completo es un esfuerzo grande. Evaluar en el doc de arquitectura si el MVP expone **un subconjunto de JMAP** (Email/get, Email/query, Email/set, Mailbox/*, con `/changes` y push) o directamente una **API propia REST/tRPC** más simple, dejando JMAP para post-MVP como interfaz de interoperabilidad (que habilitaría clientes de terceros y apps móviles).

**Fuentes:**
- https://dovecot.org/mailman3/archives/list/dovecot@dovecot.org/thread/T2JX2REPW5R4M3II7VGM5DC72ISHN777/
- https://dovecot.org/list/dovecot/2016-November/106265.html
- https://github.com/filiphanes/jmap-proxy-python
- https://github.com/stalwartlabs/mail-server
- https://tobias-weiss.org/content/devops/dovecot-vs-stalwart-imap-production-comparison/

---

## 3. Otros relevantes

### 3.1 Nextcloud Mail — **el caso de estudio más valioso del research**

Nextcloud Mail es el proyecto que **más se parece a lo que queremos hacer** (tiene sync a base propia con metadata en DB) y por eso sus fallas son nuestro mapa de minas.

| Item | Dato |
|------|------|
| **Arquitectura** | **Sync/cache**: sincroniza en background y guarda metadata en la base de Nextcloud. Es "Opción B" parcial. |
| **Stack** | PHP (app de Nextcloud) + Vue.js |
| **Licencia** | AGPL-3.0 |
| **Mantenimiento** | Activo, con Nextcloud GmbH detrás (y ahora también dueños de Roundcube). |

**Problemas documentados —leer esto como checklist de lo que NO hay que hacer:**

1. **Una conexión IMAP por carpeta sincronizada.** En cuentas con muchas carpetas, cada ciclo de sync en background dispara una ráfaga de autenticaciones IMAP simultáneas. Resultado: **efectivamente inusable con más de ~10 carpetas suscriptas** salvo que se suba muchísimo el intervalo de sync ([nextcloud/mail#12671](https://github.com/nextcloud/mail/issues/12671)).
2. **Se siente lento precisamente porque hace más trabajo en background** que un cliente normal: sincroniza detalles de mensajes en vez de solo mostrar lo que el IMAP devuelve. *El sync mal hecho es peor que no syncar.*
3. **`IMAP command too long`** y errores de sync con buzones grandes ([#6038](https://github.com/nextcloud/mail/issues/6038)).
4. Sync que **se detiene después de los mails iniciales**; errores de cron sync recurrentes en el foro.
5. Issue de performance de larga data ([#5835](https://github.com/nextcloud/mail/issues/5835)) y de sync de cuenta ([#8545](https://github.com/nextcloud/mail/issues/8545)).
6. **En 2026 están trabajando en RFC 5465 IMAP NOTIFY** para reemplazar el polling por carpeta — reconocimiento explícito de que el diseño de sync original era el problema.

**Lecciones directas para nuestro sync engine (críticas):**
- **Un pool de conexiones IMAP compartido y limitado**, jamás una conexión por carpeta. Esto es la falla #1.
- **`IMAP IDLE` / `NOTIFY` (RFC 5465)** desde el día uno, no polling. Evitar el rework que Nextcloud está haciendo 10 años después.
- **Sync incremental por `UIDVALIDITY` + `MODSEQ` (CONDSTORE/QRESYNC, RFC 7162)** — es la única forma de sincronizar buzones grandes sin re-escanear. Dovecot lo soporta bien.
- **Cuidar el largo de los comandos IMAP** (batchear UIDs en chunks) — el bug `command too long` es evitable.
- **Backpressure y sync priorizado**: INBOX primero y en caliente; carpetas frías, lazy.

**Fuentes:**
- https://github.com/nextcloud/mail/issues/12671
- https://github.com/nextcloud/mail/issues/5835
- https://github.com/nextcloud/mail/issues/8545
- https://github.com/nextcloud/mail/issues/6038
- https://help.nextcloud.com/t/nextcloud-mail-stops-syncing-after-initial-emails/218167
- https://www.systoolsgroup.com/updates/nextcloud-mail-slow/

---

### 3.2 Mailpile — el cadáver más instructivo

**Estado: técnicamente vivo, prácticamente muerto.** El propio equipo lo escribió con una honestidad brutal en el post **"Burned Out and Happy?"** (2019): el proyecto quedó **"degradado a un trabajo part-time en el mejor de los casos, y a un hobby querido en el peor"**.

Mailpile fue un caso de crowdfunding exitoso (~US$163k en 2013) con visión fuerte: webmail auto-hospedado, privacy-first, con **índice de búsqueda propio y motor de search rápido** — arquitectónicamente **muy cercano a nuestra tesis**. El desarrollo de *Mailpile 2* continúa alrededor de **Moggie** (el toolkit de búsqueda/indexación), pero a ritmo de hobby.

**La lección:** la parte difícil de Mailpile no fue técnica (el índice funcionaba) sino de **sostenibilidad humana**. Ver §6.

**Fuentes:**
- https://www.mailpile.is/blog/2019-04-06_Burnout.html
- https://github.com/mailpile/Mailpile/issues/1232
- https://github.com/mailpile/Mailpile/issues/2223
- https://www.mailpile.is/blog/

---

### 3.3 RainLoop

Mencionado solo como antecedente: fue el webmail "lindo" de su generación, quedó **semi-abandonado con CVEs sin parchear**, y la comunidad se mudó al fork SnappyMail. **Patrón clásico:** proyecto de un solo autor + éxito → autor se va → CVEs → fork. (§6)

---

### 3.4 Webmails de suites (referencia de features)

- **Zimbra:** el groupware open source "clásico" completo. Referencia de features de suite (mail + cal + contactos + tareas + docs + delegación/permisos). Su UX está anclada a los 2010s y su historia de licenciamiento (VMware → Synacor) es un caso de erosión del compromiso open source.
- **Stalwart + Bulwark:** hoy es la combinación que la prensa técnica presenta como "el stack self-hosted que parece de 2026, no de 1996". Es el conjunto contra el que nos van a comparar.
- **Mailu:** usa SnappyMail o Roundcube; se lo describe explícitamente como "funcional pero no tan pulido como SOGo" — lo cual dice mucho del piso bajo del rubro.

**Fuentes:**
- https://sumguy.com/self-hosted-email-mailcow-mailu-stalwart/
- https://profor.pro/blog/self-hosted-email-2026-mailcow-stalwart-mailu/

---

## 4. Los benchmarks comerciales: qué hace que se sientan de clase mundial

No es "diseño lindo". Son capacidades concretas que **exigen un modelo de datos con estado propio**. Ese es el argumento central de la Opción B.

### 4.1 Instant search
- **Gmail/Fastmail:** resultados en <100 ms sobre años de correo, con ranking por relevancia, no solo por fecha. Fastmail construyó su propio motor de búsqueda; Gmail indexa todo.
- **Superhuman:** búsqueda en lenguaje natural con IA, "sin operadores ni filtros que memorizar — escribí lo que buscás".
- **Requisito arquitectónico:** índice full-text invertido local (Tantivy/Meilisearch/Typesense/Postgres FTS). **`IMAP SEARCH` no puede dar esto jamás.** Este es el diferenciador #1.

### 4.2 Velocidad de render y latencia percibida
- Superhuman fijó explícitamente el objetivo de **<100 ms para cualquier acción**: abrir mail, resultado de búsqueda, navegación.
- Las técnicas reales: **optimistic UI** (la acción se pinta antes de que el servidor confirme), prefetch del mensaje siguiente, virtualización de listas, y estado local como fuente de verdad con reconciliación en background.
- **Bulwark lo aproxima con batching JMAP** (mark-read + move + fetch-next en una llamada). Nosotros podemos ir más lejos porque tenemos el store local.

### 4.3 Keyboard-first
- Superhuman: **cada acción tiene un shortcut**; usuarios avanzados procesan 100+ mails en <20 minutos.
- **`Cmd+K` / command palette** ("Superhuman Command"): si no sabés el shortcut, escribís lo que querés hacer, te muestra el shortcut para la próxima vez, y Enter lo ejecuta. **Es simultáneamente el acelerador y el mecanismo de enseñanza.** Feature de altísimo ROI.
- Gmail/Fastmail: `j`/`k` navegación, `e` archive, `#` delete, `r` reply, `/` search — vocabulario ya internalizado por los usuarios. **Copiarlo, no inventarlo.**

### 4.4 Threading / conversaciones
- Gmail popularizó la conversación como unidad primaria, **cross-folder** (el enviado aparece dentro del hilo).
- Requiere resolver `References`/`In-Reply-To` + heurísticas de subject y **persistir el thread ID**. Con IMAP-directo no se puede hacer bien de forma consistente ni performante.

### 4.5 Undo send
- Ventana de 5-30 s antes de la entrega real. **Es una cola de envío diferida en el backend**, no magia de frontend.
- Enorme impacto en confianza percibida y trivial de implementar **si tenés backend propio**. Ningún webmail IMAP-directo lo tiene bien.

### 4.6 Snooze / follow-up reminders
- Snooze: el mail desaparece del inbox y vuelve en una fecha/hora.
- **"Remind me if no reply"** (Superhuman): resucita el hilo solo si se enfría. Es de las features más queridas.
- **Requiere scheduler + estado propio.** Imposible en stateless.

### 4.7 Offline
- PWA con store local (IndexedDB) + service worker: leer, buscar, archivar y **redactar** sin conexión, con cola de acciones que se drena al reconectar.
- Bulwark lo hace vía PWA. Es tabla estándar en 2026.

### 4.8 Push real
- Cambio de estado empujado por el servidor, cero polling. Web Push para notificaciones con la pestaña cerrada.
- En nuestra arquitectura: `IMAP IDLE` en el sync engine → evento interno → SSE/WebSocket al cliente + Web Push.

### 4.9 Otras de alto valor
- **Snippets/templates con variables** disparados por teclado (Superhuman).
- **Split inbox / vistas separadas** por tipo de remitente.
- **Read statuses**, scheduled send, "send later".
- **Bloqueo de trackers/contenido externo** por defecto — donde el open source puede ganarle a Gmail por diseño, no por recursos.

**Fuentes:**
- https://superhuman.com/products/mail/shortcuts
- https://help.superhuman.com/hc/en-us/articles/45191759067411-Speed-Up-With-Shortcuts
- https://blog.superhuman.com/inbox-zero-in-7-steps/
- https://www.fastmail.help/hc/en-us/articles/360058753534-Keyboard-shortcuts
- https://www.mrtechking.com/superhuman/
- https://nickgray.net/superhuman/

---

## 5. (a) Tabla comparativa

| Proyecto | Arquitectura | Stack | Licencia | Mantenimiento (ago-2026) | Stars | Funciona con Dovecot/Mailcow | Instant search | Push real | Offline | Snooze / Undo send | Por qué no llena el hueco |
|---|---|---|---|---|---|---|---|---|---|---|---|
| **Roundcube** | IMAP-directo, DB solo settings | PHP 8 + JS/jQuery, skin Elastic | GPL-3.0 | Activo, **bus factor ~1** (Machniak). Owner: Nextcloud. 1.6.15 LTS + 1.7 RC | ~6k | ✅ Sí (estándar) | ❌ (IMAP SEARCH) | ❌ | ❌ | ❌ | Sin store ni índice propio: techo arquitectónico. UX 2015 |
| **SnappyMail** | IMAP-directo, **sin DB** | PHP 7.4+, Knockout.js | AGPL-3.0 | Activo, **1 mantenedor** (@the-djmaze) | ~1.7k | ✅ Sí | ❌ | ⚠️ SW notif. | ❌ | ❌ | El más rápido de su clase, pero misma limitación. Knockout.js = deuda |
| **SOGo** (incumbente) | IMAP-directo + groupware DB | **Obj-C/GNUstep** + AngularJS 1.x | GPL-2.0 | Activo (Inverse/Alinto), cadencia lenta | — | ✅ Nativo en Mailcow | ❌ | ❌ | ❌ | **Lento con ~3k mails** (#4011). Stack sin salida. Ancla real: CalDAV/CardDAV/**ActiveSync** |
| **Cypht** | Agregador IMAP/JMAP/EWS | PHP + JS modular | LGPL-2.1 | **Activo**, comunidad real, ~7.5k commits | ~1.7k | ✅ Sí | ❌ | ❌ | ❌ | Objetivo distinto: agregación multi-cuenta, no cliente primario pulido |
| **alps** | **Stateless por diseño** | **Go** + Lua/Go plugins | MIT | Activo, **financiado NLnet/NGI0**, prod en Migadu | ~1k | ✅ Sí | ❌ | ❌ | ❌ | Opuesto filosófico: el stateless es su virtud y nuestro techo |
| **Bulwark** | **Stateless JMAP proxy** | **Next.js 16 + React 19 + TS + Tailwind v4 + Zustand** | AGPL-3.0 | **Muy activo**, 32 releases (~1 c/2 días), v1.8.1 | ~954 | ❌ **Requiere Stalwart** | ✅ (vía Stalwart) | ✅ | ✅ PWA | ⚠️ parcial | **Nuestro benchmark de UX.** Migrar a él = tirar Mailcow entero |
| **root-fr/jmap-webmail** | Stateless JMAP | Next.js 16 + TS + Tailwind | **MIT** | Activo pero chico (~72 commits) | 215 | ❌ Requiere JMAP | ✅ (vía servidor) | ✅ | ⚠️ | ❌ | Necesita servidor JMAP. Inmaduro. **Valor: cliente JMAP TS en MIT** |
| **Twake Mail** (Linagora) | Cliente JMAP + apps móviles | Flutter/Dart + Apache James | AGPL | **Activo, respaldo corporativo fuerte** | — | ❌ Requiere Apache James | ✅ | ✅ | ✅ | ⚠️ | Acoplado a James; pieza de suite enterprise, no standalone |
| **jmapio/jmap-demo-webmail** | Demo JMAP | JS (Overture) | MIT | **Congelado** (~2015) | 126 | ❌ | — | — | — | — | Proof-of-concept, nunca fue producto |
| **Nextcloud Mail** | **Sync/cache a DB** ← más cercano | PHP + Vue.js | AGPL-3.0 | Activo (Nextcloud GmbH) | — | ✅ Sí | ⚠️ parcial | ❌ (polling; NOTIFY en curso) | ❌ | ❌ | **Sync mal diseñado**: 1 conexión IMAP por carpeta, inusable >10 carpetas |
| **Mailpile** | **Índice propio (Moggie)** | Python | AGPL-3.0 | **Hobby / part-time** (burnout declarado) | ~9k | ⚠️ | ✅ (era su fuerte) | ❌ | — | ❌ | Arquitectura afín, **muerto por sostenibilidad humana** |
| **RainLoop** | IMAP-directo | PHP + JS | AGPL | **Semi-abandonado**, CVEs sin parchear | ~4k | ✅ | ❌ | ❌ | ❌ | ❌ | Antecedente: 1 autor → abandono → fork (SnappyMail) |

**Leyenda:** ✅ tiene / ⚠️ parcial o limitado / ❌ no tiene.

**El renglón vacío de la tabla —nuestro espacio:** *sync engine + índice propio + API moderna + PWA, funcionando contra Dovecot/Mailcow.* Ningún proyecto de la lista ocupa esa fila.

---

## 6. (b) Por qué mueren los webmails open source

Patrones extraídos de los proyectos muertos, semi-muertos y en riesgo del panorama. Cada uno con su contramedida.

### Patrón 1 — La Gran Reescritura que nunca llega
**Caso testigo: Roundcube Next.** US$103.541 recaudados en 2015 para reescribir Roundcube de cero. Kolab + Roundcube **detuvieron el desarrollo en 2016**. Los backers no recibieron producto, ni updates, ni reembolso. Once años después, Roundcube sigue siendo el Roundcube viejo con un skin nuevo.

*Por qué pasa:* la reescritura total no entrega valor hasta el final, así que consume toda la energía y credibilidad antes de producir nada usable.

> **Contramedida:** entregar valor navegable desde la semana 1. El sync engine debe ser útil aun sin frontend (índice consultable). El frontend debe ser usable aun con un subconjunto de features. **Nunca un big-bang.**

### Patrón 2 — Bus factor 1
**Casos: RainLoop** (autor se fue → CVEs sin parchear → fork), **SnappyMail** (@the-djmaze, uno solo), **Roundcube** (Machniak firma casi todos los commits), **Mailpile** (burnout del equipo chico). Es el patrón **más común y más letal** de esta categoría.

*Por qué pasa en webmail específicamente:* el dominio es hostil. IMAP tiene décadas de edge cases, MIME es un pantano, la seguridad de renderizar HTML ajeno es un campo minado, y hay que soportar servidores que violan la RFC. La curva de entrada espanta contributors casuales.

> **Contramedida:** documentar arquitectura desde el día 1, tests obligatorios (ya es política del grupo), fronteras de módulos limpias (sync engine / API / frontend separables), y stack mainstream (TS/React) para maximizar el pool de gente que puede contribuir. **Evitar exactamente el error de SOGo** (Obj-C/GNUstep: pool de contributors ≈ 0).

### Patrón 3 — Burnout por expectativa de producto comercial sin ingresos comerciales
**Caso testigo: Mailpile.** El post *"Burned Out and Happy?"* es explícito. Recaudaron $163k, prometieron un producto de calidad comercial, y el mantenimiento perpetuo de un cliente de correo (soporte, seguridad, compatibilidad) excede lo que un equipo chico sostiene gratis.

*Corolario:* los proyectos que sobreviven tienen una fuente de sostenibilidad. **alps** → NLnet/NGI0 (dinero público). **Twake Mail** → Linagora (contratos enterprise). **Roundcube** → adquirido por Nextcloud. **Nextcloud Mail** → Nextcloud GmbH. **SOGo** → Inverse/Alinto. Los que no tienen ninguna, mueren.

> **Contramedida:** definir el modelo de sostenibilidad **antes** de publicar. En nuestro caso hay una ventaja estructural: **el webmail resuelve un problema real de nuestra propia operación** (Mailcow productivo, clientes reales). No dependemos de adopción externa para justificar el mantenimiento. Eso nos pone del lado sano del patrón por defecto. Post-MVP se puede evaluar hosting/soporte a terceros.

### Patrón 4 — Elegir el stack "correcto" en vez del stack "adoptado"
**Casos: SOGo** (Objective-C/GNUstep — técnicamente defendible en 2006, aislante en 2026), **SnappyMail** (Knockout.js, EOL de facto), **SOGo frontend** (AngularJS 1.x, EOL 2022).

> **Contramedida:** TypeScript + React + Postgres. Aburrido a propósito. La innovación va en la arquitectura (sync engine + índice), no en la elección de herramientas.

### Patrón 5 — Protocolo puro sin parque instalado
**Caso testigo: todo el ecosistema JMAP-standalone.** La búsqueda `jmap-webmail in:name` devuelve **7 repos en todo GitHub**, cinco de ellos con ≤17 stars. Los clientes JMAP son técnicamente superiores y comercialmente irrelevantes, porque **exigen cambiar el servidor de correo**. El único con tracción (Bulwark, ~954 stars) la consiguió pegándose a un servidor que sí tiene tracción propia (Stalwart, ~12.7k stars).

*La lección más importante del research:* **la adopción no la define la calidad del protocolo sino la compatibilidad con lo que la gente ya tiene corriendo.** Dovecot es el parque instalado gigante y no tiene JMAP ni piensa tenerlo.

> **Contramedida —y es nuestra tesis entera:** funcionar contra Dovecot/Mailcow sin pedirle al usuario que cambie nada del servidor. Es exactamente lo que ningún cliente moderno hace hoy.

### Patrón 6 — Confundir "skin nuevo" con "producto nuevo"
Roundcube tardó años en Elastic y el resultado es un Roundcube lindo con las mismas limitaciones de fondo (search, push, offline). **El techo era el modelo de datos, no el CSS.**

> **Contramedida:** el sync engine primero. Si el backend no da <100 ms de search y push real, ningún frontend lo salva.

### Patrón 7 — Sync mal hecho (peor que no syncar)
**Caso testigo: Nextcloud Mail.** Hicieron lo correcto conceptualmente (sync a base propia) y lo implementaron de forma que **una conexión IMAP por carpeta** vuelve la app inusable con >10 carpetas, y "se siente lento porque hace más trabajo en background que un cliente normal". Diez años después están migrando a IMAP NOTIFY.

> **Contramedida:** ver §3.1 — pool de conexiones acotado, QRESYNC/CONDSTORE, IDLE/NOTIFY, chunking de UIDs, sync priorizado con backpressure. **Esto es lo que hay que diseñar bien en el doc de arquitectura, y es donde el proyecto se gana o se pierde.**

---

## 7. (c) Features priorizadas: qué define "clase mundial"

Priorización por **(impacto en la percepción de calidad) ÷ (costo dado que tenemos backend propio)**. Marcadas con 🔑 las que **solo son posibles gracias a la Opción B** — son la justificación de la arquitectura y no deberían negociarse fuera del MVP.

### P0 — MVP: sin esto no es "de clase mundial", es otro webmail más

| # | Feature | Por qué | Nota de implementación |
|---|---|---|---|
| 1 | 🔑 **Instant search full-text (<100 ms)** as-you-type, con ranking por relevancia, filtros (`from:`, `has:attachment`, fechas) | **Es EL diferenciador.** Ningún competidor Dovecot-compatible lo tiene. Es la primera cosa que un usuario nota vs. SOGo | Índice propio (Tantivy / Meilisearch / Postgres FTS). Indexar headers + body + adjuntos de texto |
| 2 | 🔑 **Sync engine robusto** con IDLE/NOTIFY, QRESYNC/CONDSTORE, pool de conexiones acotado | Es el cimiento. Todo lo demás se apoya acá | **No repetir el error de Nextcloud Mail** (§3.1). Sync incremental, INBOX caliente, resto lazy |
| 3 | 🔑 **Optimistic UI** en toda acción (archivar, borrar, marcar, mover) | La latencia percibida es la métrica de calidad. Objetivo Superhuman: <100 ms | Estado local como fuente de verdad + reconciliación background + rollback si falla |
| 4 | 🔑 **Threading / conversaciones persistidas, cross-folder** | Unidad mental del usuario post-Gmail. Enviados dentro del hilo | `References`/`In-Reply-To` + heurística de subject, thread ID en base |
| 5 | **Keyboard shortcuts estilo Gmail** (`j`/`k`, `e`, `r`, `a`, `#`, `/`, `c`, `u`) | Vocabulario ya internalizado. Costo bajísimo, señal de calidad altísima | **Copiar el mapeo de Gmail/Superhuman, no inventar** |
| 6 | 🔑 **Undo send** (ventana 5-30 s configurable) | Impacto en confianza enorme / costo trivial con backend propio. Ningún webmail IMAP-directo lo tiene | Cola de envío diferida en el backend |
| 7 | 🔑 **Push real** (SSE/WebSocket desde el sync engine) | Cero polling. Mail nuevo aparece solo | IMAP IDLE → evento interno → SSE al cliente |
| 8 | **Render de mensaje seguro y rápido** con bloqueo de imágenes/trackers por defecto | Seguridad y privacidad. Terreno donde le ganamos a Gmail por diseño | Sanitización HTML estricta, CSP, proxy de imágenes opcional |
| 9 | **Compose decente**: rich text, adjuntos drag&drop, autoguardado de borrador, autocompletado de destinatarios | Piso de la categoría. Si el compose es malo, nada más importa | Autocompletado desde índice propio de remitentes/destinatarios frecuentes |
| 10 | **Lista virtualizada + prefetch del mensaje siguiente** | Es literalmente el bug #4011 de SOGo resuelto (scroll fluido con 3k+ mails) | Virtualización + ventana de prefetch |
| 11 | **Responsive / mobile-first** | El webmail se usa en el teléfono | PWA desde el arranque |

### P1 — Post-MVP temprano: lo que convierte "muy bueno" en "no vuelvo atrás"

| # | Feature | Por qué |
|---|---|---|
| 12 | 🔑 **Command palette (`Cmd+K`)** que ejecuta y **enseña** el shortcut | Feature de ROI más alto de Superhuman: acelerador + onboarding en una sola cosa |
| 13 | 🔑 **Snooze** (mail sale del inbox y vuelve en fecha/hora) | De las features más queridas. Requiere scheduler + estado |
| 14 | 🔑 **Offline real** (PWA + IndexedDB + cola de acciones) | Leer, buscar, archivar y redactar sin conexión |
| 15 | **Web Push** con la pestaña cerrada | Completa el push de P0 |
| 16 | 🔑 **"Remind me if no reply"** | Diferencial fuerte, muy querido en Superhuman |
| 17 | **Snippets/templates con variables** por teclado | Multiplicador de productividad, costo bajo |
| 18 | **Gestión de filtros Sieve con UI decente** | Mailcow/Dovecot ya tiene Sieve; solo falta una UI que no duela |
| 19 | **Multi-cuenta / unified inbox** | Relevante para nosotros (agencia multi-dominio) |
| 20 | **Scheduled send** | Extensión natural de la cola de undo send |
| 21 | **Búsqueda dentro de adjuntos (PDF/DOCX)** | Extensión natural del índice. Muy diferencial |

### P2 — Post-MVP / visión

| # | Feature | Nota |
|---|---|---|
| 22 | **API JMAP formal (RFC 8620/8621)** como interfaz pública | Habilita clientes de terceros y apps móviles. Evaluar si es MVP o P2 (§2.5) |
| 23 | **Convivencia / eventual reemplazo de CalDAV+CardDAV** | **No romper SOGo antes de resolver esto.** Es el ancla real de SOGo |
| 24 | **Split inbox / vistas por tipo de remitente** | Estilo Superhuman/Hey |
| 25 | **Búsqueda en lenguaje natural con IA** | Tenemos infra LangGraph/pgvector en VPS_atmosfera. Diferencial fuerte y aprovecha capacidades existentes del grupo |
| 26 | **Branding / theming por dominio** | Bulwark ya lo hace. Directamente útil para nuestro modelo multi-cliente |
| 27 | **S/MIME y PGP** | SnappyMail y Bulwark los tienen; es tabla estándar a mediano plazo |
| 28 | **ActiveSync (EAS)** | El más caro. Solo si hay que apagar SOGo del todo |
| 29 | **Sistema de plugins/extensiones** | Bulwark ya tiene marketplace. Post-MVP lejano |

### Anti-features (decisiones explícitas de NO hacer)

- **NO** reescribir/reemplazar Dovecot ni migrar a Stalwart. Todo el valor del proyecto está en funcionar contra el parque instalado.
- **NO** apagar SOGo en MVP — CalDAV/CardDAV/ActiveSync se quedan donde están.
- **NO** una "Gran Reescritura" ni un big-bang (Patrón 1).
- **NO** stacks exóticos por elegancia técnica (Patrón 4).
- **NO** perseguir paridad de features con SOGo. Perseguir **superioridad en el core** (leer, buscar, responder, archivar) y aceptar huecos en lo periférico.

**Fuentes de la sección:** ver §4 (benchmarks comerciales), §3.1 (lecciones Nextcloud Mail), §2.1 (mapa de features Bulwark).

---

## 8. Conclusiones para la fase de arquitectura

1. **El hueco es real y está desocupado.** No existe hoy un webmail moderno con sync engine e índice propio que funcione contra Dovecot/Mailcow. La fila está vacía en la tabla de §5.
2. **Dovecot no tiene JMAP y no lo va a tener.** Si queremos JMAP, lo construimos nosotros. Decidir en el doc de arquitectura: subconjunto JMAP en MVP vs. API propia + JMAP en P2.
3. **Bulwark es el benchmark de UX**, no el competidor. Su feature list es nuestro mapa; su dependencia de Stalwart es nuestra oportunidad.
4. **Nextcloud Mail es el mapa de minas.** Su sync mal diseñado es el error exacto que nuestro sync engine tiene que evitar, y está documentado en issues públicos.
5. **El riesgo dominante del proyecto no es técnico sino de sostenibilidad** (§6). Nuestra ventaja estructural: resolvemos un dolor propio y real (SOGo lento sobre buzones grandes, con Crash a 584 GB como caso extremo), así que el mantenimiento se justifica sin depender de adopción externa.

---

## Próximos documentos sugeridos de la serie

- `02-architecture-sync-engine.md` — diseño del sync engine IMAP→store (QRESYNC/CONDSTORE, pool de conexiones, IDLE/NOTIFY, modelo de datos, elección de motor FTS)
- `03-api-design-jmap-vs-custom.md` — decisión JMAP subset vs. API propia, con criterios y costo estimado
- `04-frontend-spec.md` — PWA React/TS, optimistic UI, command palette, mapa de shortcuts
- `05-coexistence-with-sogo.md` — plan de convivencia CalDAV/CardDAV/ActiveSync y ruta de migración de usuarios

---

*Research realizado el 2026-08-07. Todas las afirmaciones sobre estado de proyectos corresponden a esa fecha. Ninguna verificación se hizo contra el servidor de producción.*
