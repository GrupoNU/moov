# Research 03 — Superficie de integración con Mailcow

> **Proyecto:** Webmail open source Gmail-class sobre Mailcow Dockerized
> **Fecha:** 2026-08-07
> **Alcance:** research 100% documental (sin acceso al servidor de producción)
> **Aplicabilidad:** cualquier instalación de Mailcow Dockerized, no sólo la nuestra
> **Premisa de diseño:** el webmail corre como contenedor *al lado* de Mailcow, **sin modificar** Mailcow

---

## 0. Resumen ejecutivo

Mailcow no es un monolito cerrado: expone cuatro superficies de integración perfectamente utilizables desde un contenedor externo.

| Superficie | Protocolo / puerto | Uso en nuestro webmail |
|---|---|---|
| **Dovecot** | IMAP 143/993, ManageSieve 4190, LMTP 24, doveadm 12345 | Lectura de buzones, sync engine, filtros |
| **Postfix** | Submission 587 / 465 | Envío |
| **API admin** | HTTPS `/api/v1/*`, header `X-API-Key` | Aprovisionamiento, app passwords, quotas, rate limits |
| **SOGo** | HTTPS `/SOGo/dav/*` | CalDAV/CardDAV (post-MVP) |

**Hallazgo central:** el mecanismo de auth de Mailcow es un **hook HTTP centralizado** (`mailcowauth.php`). Dovecot **no** valida contra hashes locales: delega vía Lua a un endpoint HTTP que consulta MySQL/Redis. Esto significa que **cualquier credencial válida en la UI de Mailcow (password de buzón o app password) funciona en IMAP/SMTP sin configuración extra**, y que existe un ACL por protocolo (`imap_access`, `smtp_access`, …) que podemos aprovechar como control de seguridad de primera clase.

**Riesgo central:** la integración OIDC/Keycloak de Mailcow **NO cubre IMAP/SMTP** de forma nativa (no hay XOAUTH2 en Dovecot). Detalle en §1.4 y §8.

---

## 1. Autenticación

### 1.1 La cadena de auth real de Mailcow (crítico)

Esto es lo más importante del informe y no está bien documentado en docs.mailcow.email — sale de leer el código.

`data/conf/dovecot/dovecot.conf` define **tres passdb en orden**:

```
# 1) passdb lua con cache — el camino normal
passdb {
  driver = lua
  args = file=/etc/dovecot/auth/passwd-verify.lua blocking=yes cache_key=%s:%u:%w
  result_success = return-ok
  result_failure = continue
  result_internalfail = continue
}
# 2) master password (passwd-file)
passdb {
  driver = passwd-file
  args = /etc/dovecot/dovecot-master.passwd
  master = yes
  skip = authenticated
}
# 3) lua de nuevo (obligatorio, sin cache)
passdb {
  driver = lua
  args = file=/etc/dovecot/auth/passwd-verify.lua blocking=yes
}
```

`passwd-verify.lua` hace un **POST HTTPS a `https://nginx:9082`** con el JSON:

```json
{ "username": "...", "password": "...", "real_rip": "...", "service": "imap" }
```

Ese endpoint es `mailcowauth.php`, que intenta **en cascada**:

1. **SSO de SOGo** — sólo si `real_rip == <IPV4_NETWORK>.248` (la IP fija del contenedor SOGo). Compara contra `/etc/sogo-sso/sogo-sso.pass`.
2. **`apppass_login()`** — app passwords, con verificación de protocolo.
3. **`user_login()`** — password normal del buzón, incluyendo el desvío a identity providers (Keycloak/LDAP/OIDC).

**Consecuencias de diseño para nosotros:**

- **No necesitamos tocar nada de Mailcow para autenticar.** Si un usuario tiene password válido o app password, un `IMAP LOGIN` desde nuestro contenedor funciona.
- El truco de SSO de SOGo (paso 1) **no es replicable de forma limpia**: está atado por IP a `.248`. Suplantar esa IP para saltarse la password sería un backdoor y viola la premisa de "no modificar Mailcow". **Descartado.**
- El `service` que Dovecot envía (`imap`, `pop3`, `sieve`, `smtp`) se usa para **ACL por protocolo**. Si un buzón tiene `imap_access = 0`, el login IMAP falla aunque la password sea correcta.

Fuentes:
- https://github.com/mailcow/mailcow-dockerized/blob/master/data/conf/dovecot/dovecot.conf
- https://github.com/mailcow/mailcow-dockerized/blob/master/data/conf/dovecot/auth/passwd-verify.lua
- https://github.com/mailcow/mailcow-dockerized/blob/master/data/conf/dovecot/auth/mailcowauth.php
- https://github.com/mailcow/mailcow-dockerized/blob/master/data/web/inc/functions.auth.inc.php

### 1.2 Mecanismos SASL disponibles

```
auth_mechanisms = plain login
disable_plaintext_auth = yes
ssl_min_protocol = TLSv1.2
```

- Sólo **PLAIN** y **LOGIN**. **No hay CRAM-MD5, DIGEST-MD5 ni XOAUTH2.**
- `disable_plaintext_auth = yes` ⇒ obligatorio TLS (implícito en 993, o STARTTLS en 143).
- Para nuestro backend: `AUTHENTICATE PLAIN` sobre TLS es el único camino. Simple y suficiente.

### 1.3 App passwords — el mecanismo recomendado

`apppass_login()` valida contra la tabla `app_passwd`, con **ACL por protocolo**:

| Flag | Protocolo |
|---|---|
| `imap_access` | IMAP |
| `smtp_access` | SMTP submission |
| `pop3_access` | POP3 |
| `sieve_access` | ManageSieve |
| `dav_access` | CalDAV/CardDAV (SOGo) |
| `eas_access` | ActiveSync |

Se crean vía API (§2). **Esto es oro para nosotros:** podemos emitir por usuario una app password dedicada al webmail con **exactamente** los protocolos que necesitamos (`imap` + `smtp` + `sieve`, y `dav` post-MVP), revocable de forma independiente sin afectar la password del usuario ni sus otros clientes.

```
POST /api/v1/add/app-passwd
{
  "active": "1",
  "username": "info@domain.tld",
  "app_name": "webmail-gruponu",
  "app_passwd": "<generada>",
  "app_passwd2": "<misma>",
  "protocols": ["imap_access", "smtp_access", "sieve_access"]
}
```

Nota: `app_passwd` se **envía en claro** al crear y **no se puede releer** después (se guarda hasheada). Hay que capturarla en el momento y guardarla cifrada de nuestro lado.

### 1.4 OIDC / Keycloak / LDAP — el gran asterisco

Mailcow soporta tres identity providers: **Keycloak, Generic OIDC y LDAP**.

**El punto crítico: OIDC no cubre los protocolos de mail.** Los propios desarrolladores lo explican: querían OIDC puro pero chocaron con SMTP/IMAP, porque **no hay browser en un login IMAP** — el Authorization Code Flow es imposible ahí.

Las salidas que implementaron:

| Provider | Qué pasa con IMAP/SMTP |
|---|---|
| **Generic OIDC** | El usuario **debe crear app passwords**. No hay otra vía. |
| **Keycloak + Mailpassword Flow** | Keycloak guarda un atributo `mailcow_password` (hash) que `mailcowauth` verifica directamente. Login IMAP con la password del IdP funciona, **sin** protocolo OIDC de por medio. |
| **LDAP** | El bind LDAP se puede hacer con las credenciales ⇒ funciona en IMAP. |

Sobre el ROPC (Resource Owner Password Credentials): Mailcow lo evalúa pero está **omitido de OAuth 2.1 y en camino a deprecarse**, por eso prefirieron el atributo hasheado.

**Implicancia para el webmail:** **no debemos apoyarnos en OIDC de Mailcow para el login del webmail.** Si algún día un cliente usa Keycloak, la ruta soportada es Mailpassword Flow o app passwords — ambas terminan en un `IMAP LOGIN` con user+password, que es exactamente lo que nuestro diseño ya hace. **Nuestra arquitectura de auth es agnóstica al IdP.** Eso es una ventaja, no una limitación.

Aparte: Mailcow puede actuar **como** OAuth2 provider (`/oauth/authorize.php`, `/oauth/profile.php`, y `POST /api/v1/add/oauth2-client`). Sirve para "login con Mailcow" en apps web de terceros — **pero sólo da identidad, no acceso al buzón**. Podríamos usarlo para el SSO web de nuestro webmail, pero seguiríamos necesitando una credencial IMAP para leer el mail. Ver §8, decisión abierta D2.

Fuentes:
- https://mailcow.email/posts/2023/mailcow-idp/
- https://docs.mailcow.email/manual-guides/mailcow-UI/u_e-mailcow_ui-keycloak/
- https://deepwiki.com/mailcow/mailcow-dockerized/5.5-identity-provider-integration

### 1.5 Master user de Dovecot

Permite acceder a **cualquier** buzón sin la password del dueño.

**Modo estático** (en `mailcow.conf`):

```
DOVECOT_MASTER_USER=mymasteruser
DOVECOT_MASTER_PASS=mysecretpass
```

Aplicar con `docker compose up -d` (**no** `restart` — no relee `mailcow.conf`).

Login (separador `*`, definido por `auth_master_user_separator = *`):

```
usuario: test@example.org*mymasteruser@mailcow.local
password: mysecretpass
```

Notas:
- Si no se definen, Mailcow **genera credenciales aleatorias en cada arranque de Dovecot** (comportamiento por defecto y recomendado por upstream).
- **No sirve para SOGo.**
- Requiere editar `mailcow.conf` ⇒ roza la premisa de "no modificar Mailcow" (es config, no código, pero es un cambio en su árbol).

**Recomendación: NO usarlo como camino principal.** Un master password es una llave maestra de todos los buzones de todos los dominios; si nuestro contenedor se compromete, se compromete el servidor entero. Reservarlo para tareas administrativas offline (migraciones, reindexado), nunca para el path de request de usuario.

Fuente: https://docs.mailcow.email/manual-guides/Dovecot/u_e-dovecot-static_master/

### 1.6 Comparativa de caminos de auth

| Opción | Aísla por usuario | Revocable | Toca Mailcow | Veredicto |
|---|---|---|---|---|
| **Password del usuario, pass-through** | Sí | n/a | No | ✅ MVP |
| **App password dedicada por usuario** | Sí | Sí, individual | No (sólo API) | ✅✅ **Recomendado** |
| Master user estático | **No** (llave maestra) | Sólo global | Sí (`mailcow.conf`) | ⚠️ Sólo admin offline |
| OIDC/Keycloak directo a IMAP | — | — | — | ❌ No existe |
| Suplantar SSO de SOGo por IP | — | — | Sí | ❌ Backdoor |

**Camino recomendado (dos fases):**

1. **Login:** el usuario entrega email+password a *nuestro* backend. Validamos haciendo un `IMAP LOGIN` real contra `dovecot:143` con STARTTLS. Si Dovecot dice OK, la credencial es válida — no replicamos lógica de auth ni tocamos la DB de Mailcow.
2. **Aprovisionamiento:** en ese primer login exitoso, creamos vía API una app password `webmail-<instancia>` con scope `imap+smtp+sieve`, la guardamos cifrada (envelope encryption), y **descartamos la password del usuario**. A partir de ahí el sync engine trabaja en background con la app password, sin necesidad de que el usuario esté online.

Esto resuelve el problema clásico del webmail con sync engine: **para sincronizar en background hace falta una credencial persistente**, y guardar la password del usuario es inaceptable. La app password es exactamente el mecanismo que Mailcow provee para esto, revocable desde la propia UI de Mailcow por el usuario.

---

## 2. API admin de Mailcow

### 2.1 Autenticación y transporte

- **Base:** `https://<MAILCOW_HOSTNAME>/api/v1/`
- **Header:** `X-API-Key: <clave>` (esquema `ApiKeyAuth`, `type: apiKey`, `in: header`)
- **Spec:** OpenAPI 3.1.0 en `data/web/api/openapi.yaml` (~206 KB), navegable en la UI en `/api/`
- Claves configurables en la UI (**Configuration → Access → API**), con tipos **read-only** y **read-write**, y **allowlist de IPs** por clave.

**Nota de seguridad:** la allowlist de IP es la defensa clave. Nuestro contenedor está en `mailcow-network` con IP conocida ⇒ restringir la clave a esa IP. Además hay `POST /api/v1/edit/cors` para la política CORS.

### 2.2 Endpoints relevantes

**Aprovisionamiento y lectura:**

| Endpoint | Uso |
|---|---|
| `GET /api/v1/get/domain/all` | Listar dominios (branding multi-tenant) |
| `GET /api/v1/get/mailbox/all` | Listar buzones, quota, atributos |
| `GET /api/v1/get/alias/all` | Aliases → identidades "enviar como" |
| `POST /api/v1/add/mailbox`, `edit`, `delete` | Alta/baja |
| `POST /api/v1/add/alias`, `add/time_limited_alias` | Aliases; los time-limited son alias desechables tipo "Hide My Email" |
| `POST /api/v1/edit/mailbox/custom-attribute` | **Guardar preferencias del webmail en el propio Mailcow** |

**App passwords:**

| Endpoint | Uso |
|---|---|
| `POST /api/v1/add/app-passwd` | Emitir credencial del webmail (§1.3) |
| `POST /api/v1/delete/app-passwd` | Revocar (logout de dispositivo) |

**Operación:**

| Endpoint | Uso |
|---|---|
| `POST /api/v1/edit/rl-mbox/`, `rl-domain/` | Rate limits de envío (§4) |
| `GET /api/v1/get/quarantine/all` | Cuarentena — UI de "spam retenido" |
| `POST /api/v1/edit/spam-score/` | Umbral de spam por usuario |
| `GET /api/v1/get/status/containers`, `/vmail`, `/version` | Health checks y detección de versión |
| `POST /api/v1/edit/domain/footer` | Firmas corporativas por dominio |
| `POST /api/v1/add/syncjob` | Imapsync — migración de cuentas externas |
| `POST /api/v1/edit/pushover` | Notificaciones push |
| `POST /api/v1/add/oauth2-client` | Registrar nuestro webmail como cliente OAuth2 |

**Ausencia relevante:** **no hay endpoint de Sieve en la API.** Los filtros se manejan por **ManageSieve (4190)**, no por REST (§3.4).

Fuente: https://github.com/mailcow/mailcow-dockerized/blob/master/data/web/api/openapi.yaml

### 2.3 Qué automatizamos

- **Onboarding sin fricción:** primer login → app password automática, sin que el usuario toque la UI de Mailcow.
- **Branding por dominio:** `get/domain/all` + tabla propia de tema (logo, colores) ⇒ multi-tenant desde un solo contenedor.
- **Identidades:** `get/alias/all` alimenta el selector de remitente.
- **Quota widget:** de `get/mailbox/all`.
- **Preferencias:** `edit/mailbox/custom-attribute` evita una DB propia para settings simples (aunque para el sync engine sí necesitamos DB propia).

---

## 3. Dovecot en Mailcow

### 3.1 Protocolos y puertos

```
protocols = imap sieve lmtp pop3
```

| Servicio | Puerto interno | Publicado en host |
|---|---|---|
| IMAP | 143 | `${IMAP_PORT:-143}` |
| IMAPS | 993 | `${IMAPS_PORT:-993}` |
| POP3 / POP3S | 110 / 995 | sí |
| **ManageSieve** | **4190** | `${SIEVE_PORT:-4190}` |
| LMTP | 24 | no (interno) |
| **doveadm** | **12345** | `${DOVEADM_PORT:-127.0.0.1:19991}` (**sólo localhost**) |
| auth-inet | 10001 | no |
| Variantes haproxy | 10143/10993/10110/10995/14190 | no |

**Desde `mailcow-network` nos conectamos a `dovecot:143`** (alias de red del contenedor). El puerto 143 con STARTTLS es preferible al 993 dentro de la red Docker: menos overhead de handshake, y el tráfico no sale del bridge. `disable_plaintext_auth = yes` obliga igual a STARTTLS, así que no perdemos cifrado.

### 3.2 Plugins activos — lista definitiva

De `data/Dockerfiles/dovecot/docker-entrypoint.sh` (se escriben en runtime según `SKIP_FTS`):

**Con `SKIP_FTS=n` (FTS activo):**

```
mail_plugins      = quota acl zlib mail_crypt mail_crypt_acl mail_log notify
                    fts fts_flatcurve listescape replication lazy_expunge
mail_plugins_imap = quota imap_quota imap_acl acl zlib imap_zlib imap_sieve
                    mail_crypt mail_crypt_acl notify mail_log
                    fts fts_flatcurve listescape replication
mail_plugins_lmtp = quota sieve acl zlib mail_crypt mail_crypt_acl
                    fts fts_flatcurve notify listescape replication
```

Con `SKIP_FTS=y` es idéntico menos `fts fts_flatcurve`.

### 3.3 Capabilities para el sync engine — evaluación

| Capability | Estado | Nota |
|---|---|---|
| **IDLE** | ✅ Core | Push en tiempo real. Una conexión IDLE por carpeta observada. |
| **CONDSTORE / QRESYNC** | ✅ Core en Dovecot 2.3+ | **Críticos.** No requieren plugin; Dovecot los ofrece de fábrica sobre Maildir con índices. Verificar con `a CAPABILITY`. |
| **SPECIAL-USE** | ✅ Configurado explícitamente | `dovecot.folders.conf` mapea `\Trash \Archive \Sent \Drafts \Junk` con decenas de traducciones (alemán, ruso, chino, griego…). **No hardcodear nombres de carpeta** — usar SPECIAL-USE. |
| **Keywords (tags/labels)** | ✅ Maildir | Maildir soporta keywords arbitrarias, pero **limitado a 26 por carpeta** (`a`–`z` en el nombre del archivo). Restricción real para un modelo de labels tipo Gmail. Ver §8, riesgo R3. |
| **NOTIFY (RFC 5465)** | ⚠️ Dudoso | Existe el plugin `notify`, pero **es el plugin interno de Dovecot** (eventos para otros plugins), **no** la extensión IMAP NOTIFY. Dovecot CE históricamente **no** implementa IMAP NOTIFY. **Asumir que no está y diseñar con IDLE.** |
| **MOVE, UIDPLUS, ESEARCH, LIST-EXTENDED, SORT, THREAD** | ✅ Core | Estándar en Dovecot 2.3. |
| **COMPRESS=DEFLATE** | ✅ `imap_zlib` | Reduce ancho de banda del sync inicial. |
| **QUOTA** | ✅ `imap_quota` | Quota vía IMAP sin llamar a la API. |
| **ACL** | ✅ `imap_acl` | Buzones compartidos. |
| **METADATA** | ✅ `imap_metadata = yes` | Con `mail_attribute_dict = file:%h/dovecot-attributes`. **Permite guardar estado del webmail server-side por buzón.** |

**Detalle de performance ya activo:**

```
maildir_very_dirty_syncs = yes   # acelera buzones muy grandes
mail_prefetch_count = 30
```

⚠️ `maildir_very_dirty_syncs = yes` implica que **no se deben modificar los archivos de `cur/` por fuera de Dovecot**. Nuestro webmail **nunca debe tocar el filesystem de vmail directamente** — todo por IMAP. Regla dura.

### 3.4 ManageSieve — filtros

- Puerto **4190** (publicado) / 14190 (haproxy).
- Extensiones: `sieve_extensions = +notify +imapflags +vacation-seconds +editheader`; globales `+vnd.dovecot.pipe +vnd.dovecot.execute`.
- Scripts de usuario en `/var/vmail/sieve/%u.sieve`.
- **Mailcow usa `sieve_before`/`sieve_after` desde un dict SQL** (`sieve_before2 = dict:proxy::sieve_before;name=active`) — así implementa sus filtros globales de la UI. Nuestros scripts de usuario conviven sin conflicto, pero **los globales de Mailcow corren antes/después** y pueden ganar. A tener en cuenta al debuggear "mi filtro no se aplica".
- `imapsieve` está enganchado a la carpeta `Junk`: **mover un mail a Junk dispara el reporte de spam a Rspamd automáticamente**, y sacarlo de Junk reporta ham. Nuestro botón "marcar como spam" debe ser simplemente un `MOVE` a la carpeta con SPECIAL-USE `\Junk` — el aprendizaje del filtro es gratis.

### 3.5 FTS: Flatcurve (⚠️ Solr fue eliminado)

**Cambio importante que afecta nuestra documentación interna:** en el update **Janmooary 2025**, Mailcow **reemplazó Solr por Flatcurve** (basado en Xapian). Solr ya no existe; el update **borra las variables `SOLR_*` de `mailcow.conf`** y ofrece eliminar los volúmenes.

| Variable | Default | Nota |
|---|---|---|
| `SKIP_FTS` | `y` en `docker-compose.yml`; docs dicen `n` | **Verificar en la instalación concreta** |
| `FTS_HEAP` | 512 (compose) / 128 (docs) | MB por proceso |
| `FTS_PROCS` | 3 (compose) / 1 (docs) | ~mitad de los cores |

- Índices en el volumen `vmail-index-vol-1` (no un contenedor aparte, a diferencia de Solr).
- Indexa automáticamente al llegar 20+ mails o al ejecutar una búsqueda.
- Job de optimización diario vía Ofelia (`optimize-fts.sh`, `0 0 0 * * *`).

**Decisión búsqueda (§8, D1):** Flatcurve responde a `IMAP SEARCH` server-side. Delegarle la búsqueda **full-text del cuerpo** es lo correcto: cero storage duplicado, cero problema de consistencia, y respeta el cifrado de Mailcow. Pero Flatcurve **no da ranking por relevancia ni búsqueda instantánea tipo Gmail**. Estrategia híbrida recomendada:

- **Nuestro índice** (SQLite FTS5 / Postgres por usuario): headers, remitente, asunto, participantes, fechas, labels. Es lo que alimenta el autocompletado y el 90% de las búsquedas reales, y es rápido porque ya tenemos esos metadatos en la DB del sync engine.
- **Flatcurve vía `IMAP SEARCH TEXT/BODY`**: full-text del cuerpo bajo demanda, cuando el usuario busca algo que no está en los metadatos.

⚠️ Si `SKIP_FTS=y`, `SEARCH BODY` degrada a **scan secuencial de todos los mensajes** — devastador en buzones de cientos de GB (como los 584 GB de Crash). **El webmail debe detectar la capability FTS y desactivar/advertir la búsqueda de cuerpo si no está.**

### 3.6 doveadm HTTP API

Sí existe: `doveadm_port = 12345`, con `doveadm_password` a definir en `data/conf/dovecot/extra.conf`. Da una API HTTP/JSON para operaciones administrativas (search, fetch, force-resync, quota, index).

**Pero:** en `docker-compose.yml` está publicado como `127.0.0.1:19991` — **sólo localhost del host**, no accesible desde otro contenedor por esa vía. Desde `mailcow-network` sí llegaríamos a `dovecot:12345` directamente. Requiere setear `doveadm_password` en `extra.conf` (archivo de overrides **previsto por Mailcow** para esto — es el mecanismo soportado, no un hack).

**Recomendación: no usarlo en el MVP.** Es una credencial administrativa global equivalente al master user. IMAP cubre todo lo que necesitamos. Reservar para operaciones batch offline (`force-resync`, `index`).

---

## 4. Envío

### 4.1 Submission

| Puerto | Modo |
|---|---|
| 587 | STARTTLS (submission) — **recomendado** |
| 465 | SMTPS implícito |
| 25 | MTA-to-MTA, **no usar** |

Desde nuestro contenedor: `postfix:587` (alias de red).

Credenciales: **las mismas del buzón o la app password con `smtp_access`**. Postfix delega en el mismo `mailcowauth` vía Dovecot SASL, con `service = smtp`.

Ventaja de usar submission en vez de inyectar por LMTP/sendmail: Postfix aplica **todo el pipeline de salida de Mailcow** — firma DKIM, rate limits, políticas TLS de salida, BCC maps, footers de dominio, y el mail queda en el log de Mailcow. Si inyectáramos por otro lado, perderíamos DKIM. **No hay razón para no usar 587.**

### 4.2 Sent folder — responsabilidad del cliente

**Postfix NO copia a `Sent`.** Es responsabilidad del cliente. Nuestro flujo por cada envío:

1. `SMTP` a `postfix:587` → entrega.
2. `IMAP APPEND` a la carpeta con SPECIAL-USE `\Sent`, con flag `\Seen`.

⚠️ Estos dos pasos **no son atómicos**. Si el APPEND falla tras un envío exitoso, el mail se fue pero no aparece en Enviados. Necesitamos un **outbox transaccional**: persistir el mensaje localmente, marcar `sent_at` tras el SMTP, y reintentar el APPEND por separado con backoff. Nunca reintentar el SMTP tras un 250 (duplicaría el envío).

⚠️ Si el usuario tiene también SOGo o Thunderbird activo, ambos podrían hacer APPEND ⇒ **duplicados en Enviados**. Mitigación: comparar `Message-ID` antes del APPEND.

### 4.3 Rate limits

Implementados en **Rspamd**, no en Postfix (`data/conf/rspamd/lua/ratelimit.lua`): un símbolo `DYN_RL` inyecta el valor traído de Redis, con fallback **usuario → dominio**.

Vía API:

```
POST /api/v1/edit/rl-mbox/
{ "attr": { "rl_value": "10", "rl_frame": "h" }, "items": ["info@domain.tld"] }
```

`rl_frame`: `s` (segundo), `m` (minuto), `h` (hora). `-1` = desactivado.

**Para el webmail:** al exceder el límite, Postfix devuelve un error temporal. La UI debe traducirlo a un mensaje humano ("alcanzaste tu límite de envío, reintentaremos en X") y **encolar con reintento**, no descartar ni mostrar un 4xx crudo. Conviene leer el rate limit del usuario por API para avisar *antes* de un envío masivo.

---

## 5. CalDAV / CardDAV de SOGo (post-MVP)

### 5.1 URLs

Del template nginx de Mailcow (`sites-default.conf.j2`):

```
rewrite ^/.well-known/caldav$  /SOGo/dav/ permanent;
rewrite ^/.well-known/carddav$ /SOGo/dav/ permanent;
location ^~ /principals { return 301 /SOGo/dav; }
location ^~ /Microsoft-Server-ActiveSync { ... }
location ^~ /SOGo { ... }
```

| Recurso | URL |
|---|---|
| Discovery | `https://<host>/.well-known/caldav` → `/SOGo/dav/` |
| Principal | `https://<host>/SOGo/dav/<usuario@dominio>/` |
| Calendario personal | `https://<host>/SOGo/dav/<usuario@dominio>/Calendar/personal/` |
| Contactos personales | `https://<host>/SOGo/dav/<usuario@dominio>/Contacts/personal/` |
| ActiveSync | `https://<host>/Microsoft-Server-ActiveSync` |

Descubrimiento correcto: `PROPFIND` sobre `.well-known/caldav` → seguir `current-user-principal` → `calendar-home-set`. **No hardcodear `/personal/`** (el usuario puede tener varios calendarios).

### 5.2 Autenticación

**HTTP Basic sobre TLS** — con la password del buzón o una app password con **`dav_access`**. Nginx valida con `auth_request /sogo-auth-verify` (subrequest interna a `127.0.0.1:65510/sogo-auth`) y luego inyecta a SOGo:

```
proxy_set_header x-webobjects-remote-user "$user";
proxy_set_header Authorization "$auth";
proxy_set_header x-webobjects-auth-type "$auth_type";
```

Es el patrón clásico de proxy auth de SOGo. Nosotros entramos por el frente (Basic contra nginx), sin necesidad de conocer ese mecanismo interno — **misma app password, agregando `dav_access`**.

Para MVP: **sólo lectura de free/busy y eventos próximos** en la barra lateral. Escritura de calendario es un proyecto en sí (recurrencias, timezones, invitaciones iTIP).

---

## 6. Networking y deploy

### 6.1 La red

```yaml
networks:
  mailcow-network:
    driver: bridge
    ipam:
      config:
        - subnet: ${IPV4_NETWORK:-172.22.1}.0/24
        - subnet: ${IPV6_NETWORK:-fd4d:6169:6c63:6f77::/64}
```

Con `COMPOSE_PROJECT_NAME=mailcow-dockerized`, el nombre real es **`mailcowdockerized_mailcow-network`** (Compose elimina guiones del proyecto).

**Aliases de red útiles:** `dovecot` (.250), `postfix`, `nginx`, `mysql`, `redis`, `sogo` (.248), `rspamd`, `unbound` (.254).

⚠️ IPs fijas ocupadas: `.248` sogo, `.249`, `.250` dovecot, `.253`, `.254` unbound. **No asignar IP fija a nuestro contenedor** — usar DNS por alias y dejar que Docker asigne.

### 6.2 Dos patrones de deploy

**Patrón A — `docker-compose.override.yml` (el que usa Mailcow para add-ons)**

En `/opt/mailcow-dockerized/docker-compose.override.yml`:

```yaml
services:
  webmail-app:
    image: gruponu/webmail:latest
    restart: always
    environment:
      - IMAP_HOST=dovecot
      - IMAP_PORT=143
      - SMTP_HOST=postfix
      - SMTP_PORT=587
      - SIEVE_HOST=dovecot
      - SIEVE_PORT=4190
      - MAILCOW_API_URL=http://nginx/api/v1
      - MAILCOW_API_KEY=${WEBMAIL_API_KEY}
    volumes:
      - webmail-data-vol-1:/data
    networks:
      mailcow-network:
        aliases:
          - webmail

volumes:
  webmail-data-vol-1:
```

Ventajas: arranca/para con Mailcow, resolución DNS directa por alias, es el mecanismo **oficialmente documentado** por Mailcow (lo usan para Mailman 3 y para integrar Traefik).
Desventajas: nuestro ciclo de vida queda atado a `mailcow-dockerized`; `./update.sh` puede advertir sobre el override (no lo borra, pero conviene versionarlo fuera y symlinkear).

**Patrón B — stack propio + red externa (recomendado)**

En `/opt/webmail/docker-compose.yml`:

```yaml
services:
  webmail-app:
    image: gruponu/webmail:latest
    networks:
      - mailcow-network
      - webmail-internal

networks:
  mailcow-network:
    external: true
    name: mailcowdockerized_mailcow-network
  webmail-internal:
    driver: bridge
```

**Preferimos B.** Separa ciclos de vida (deploy del webmail sin tocar Mailcow, y `./update.sh` de Mailcow no nos ve), mantiene los repos independientes, y da una red interna propia para nuestra DB/cache sin exponerla a los contenedores de Mailcow. La contra —el nombre de red hardcodeado, que depende de `COMPOSE_PROJECT_NAME`— se resuelve con una variable de entorno documentada en el runbook.

### 6.3 Exposición HTTP

Tres opciones:

1. **Subdominio propio con reverse proxy propio** (`webmail.<dominio>`) — **recomendado.** Cero riesgo de tocar la config de nginx de Mailcow, TLS independiente, y no compite por los puertos 80/443 que Mailcow ya toma.
2. Path dentro de nginx de Mailcow (`/webmail`) — requiere modificar sus templates. **Viola la premisa. Descartado.**
3. Reverse proxy delante de ambos — es el patrón documentado por Mailcow para Traefik/nginx-proxy; viable pero implica reconfigurar el frontend de Mailcow (`HTTP_BIND`/`HTTPS_BIND`).

Nota para nuestra instalación: Mailcow ocupa 80/443 en la IP-A. Un `webmail.<dominio>` necesita su propia terminación TLS — o bien en otra IP, o bien con la opción 3.

Fuentes:
- https://github.com/mailcow/mailcow-dockerized/blob/master/docker-compose.yml
- https://docs.mailcow.email/third_party/mailman3/third_party-mailman3/
- https://docs.mailcow.email/post_installation/firststeps-rp/

---

## 7. Diagrama de integración propuesto

```
                          Internet
                             │
              ┌──────────────┴───────────────┐
              │                              │
     webmail.dominio.tld            mail.dominio.tld
     (TLS propio)                   (nginx de Mailcow, intacto)
              │                              │
┌─────────────▼──────────────┐               │
│  webmail-frontend (SPA)    │               │
│  React/Svelte + SW         │               │
└─────────────┬──────────────┘               │
              │ HTTPS/JSON + WebSocket        │
┌─────────────▼───────────────────────────┐  │
│  webmail-backend                        │  │
│  ┌───────────────────────────────────┐  │  │
│  │ Auth service                      │  │  │
│  │  · IMAP LOGIN → valida credencial │  │  │
│  │  · emite app password vía API     │  │  │
│  │  · sesión JWT propia              │  │  │
│  ├───────────────────────────────────┤  │  │
│  │ Sync engine                       │  │  │
│  │  · IDLE por carpeta               │  │  │
│  │  · CONDSTORE/QRESYNC delta        │  │  │
│  │  · pool de conexiones por usuario │  │  │
│  ├───────────────────────────────────┤  │  │
│  │ Outbox (transaccional)            │  │  │
│  │  · SMTP → APPEND a \Sent          │  │  │
│  ├───────────────────────────────────┤  │  │
│  │ Search: metadatos local           │  │  │
│  │         + SEARCH TEXT → Flatcurve │  │  │
│  └───────────────────────────────────┘  │  │
└──┬────────┬────────┬─────────┬──────────┘  │
   │        │        │         │             │
   │ IMAP   │ SMTP   │ Sieve   │ REST        │
   │ :143   │ :587   │ :4190   │ X-API-Key   │
   │        │        │         │             │
═══▼════════▼════════▼═════════▼═════════════▼═══
        mailcowdockerized_mailcow-network
═══▲════════▲════════▲═════════▲═════════════▲═══
   │        │        │         │             │
┌──┴────────┴────────┴──┐  ┌───┴─────────────┴──┐
│  dovecot (.250)       │  │  nginx  →  php-fpm │
│   IMAP/Sieve/LMTP     │  │  /api/v1  /SOGo    │
│   passdb lua ─────────┼──┼─→ :9082            │
│   Flatcurve FTS       │  │      mailcowauth   │
└───────┬───────────────┘  └────────┬───────────┘
        │                           │
┌───────▼──────┐  ┌─────────────┐  ┌▼────────────┐
│ postfix :587 │  │ vmail-vol-1 │  │ mysql/redis │
│  DKIM, RL    │  │  (Maildir)  │  │             │
└──────────────┘  └─────────────┘  └─────────────┘

┌──────────────────────────────┐
│  webmail-internal (privada)  │
│   postgres · redis (nuestro) │
└──────────────────────────────┘

REGLA DURA: acceso a mail SIEMPRE por IMAP.
NUNCA tocar vmail-vol-1 en el filesystem
(maildir_very_dirty_syncs = yes lo prohíbe).
```

**Flujo de login:**

```
Usuario → webmail: email + password
  webmail → dovecot:143 : STARTTLS + LOGIN
      dovecot → passwd-verify.lua → nginx:9082 → mailcowauth.php
          → apppass_login() → user_login() → MySQL
      ← OK
  webmail → nginx/api/v1/add/app-passwd (X-API-Key)
      ← app_passwd en claro (única vez)
  webmail: cifra y guarda; descarta la password del usuario
  webmail → usuario: JWT de sesión
```

---

## 8. Decisiones abiertas y riesgos

### Decisiones abiertas

**D1 — Búsqueda: ¿Flatcurve o índice propio?**
Recomendación: **híbrido** (§3.5). Metadatos en índice propio (rápido, ranking nuestro); cuerpo delegado a `IMAP SEARCH TEXT` sobre Flatcurve. Pendiente: benchmark de latencia de `SEARCH` en un buzón de 100+ GB antes de comprometer la UX. Si Flatcurve resulta lento, la alternativa es indexar el cuerpo nosotros — con el costo de duplicar storage y romper el modelo de cifrado de Mailcow (`mail_crypt`).

**D2 — ¿Usamos Mailcow como OAuth2 provider para el login web?**
Daría "login con Mailcow" (§1.4), pero **no** da acceso al buzón: igual haría falta una credencial IMAP. Sólo tendría sentido combinado con app passwords pre-aprovisionadas por el admin. **Propuesta: fuera del MVP.**

**D3 — Modelo de labels con el límite de 26 keywords.**
Ver R3. Opciones: (a) labels como carpetas IMAP (compatible con todos los clientes, pero sin multi-label real); (b) keywords hasta 26 + overflow en nuestra DB (rompe la consistencia con otros clientes); (c) híbrido: las 26 más usadas como keywords, el resto sólo en nuestra UI. **Decidir antes de diseñar el schema.**

**D4 — Patrón de deploy: A u B.** Propuesta: **B** (§6.2). Confirmar con Diego, porque implica un segundo stack a operar.

**D5 — ¿Persistimos la password del usuario si la API no está disponible?** Si el aprovisionamiento de app password falla, ¿degradamos a guardar la password cifrada, o bloqueamos el login? **Propuesta: bloquear** y mostrar error — guardar passwords de usuario es una deuda de seguridad que no queremos.

### Riesgos

**R1 — OIDC de Mailcow no cubre IMAP. (Alto impacto, ya mitigado)**
No existe XOAUTH2 en Dovecot de Mailcow; los mecanismos son sólo `plain login`. Si un cliente exige SSO corporativo real para el mail, la única vía soportada es Keycloak + Mailpassword Flow o app passwords. **Mitigación:** nuestra arquitectura ya es agnóstica al IdP (siempre terminamos en `IMAP LOGIN`). **No es bloqueante, pero hay que comunicarlo bien comercialmente** — "SSO" significará SSO en nuestro webmail, no en IMAP.

**R2 — App passwords no se pueden releer. (Medio)**
Si perdemos nuestra copia cifrada, hay que re-aprovisionar (nuevo login del usuario). **Mitigación:** backup cifrado + flujo de re-auth transparente.

**R3 — Límite de 26 keywords por carpeta en Maildir. (Medio-alto para labels)**
Restricción de formato del propio Maildir, no de Mailcow. Un modelo de labels ilimitado tipo Gmail **no es directamente representable**. Ver D3.

**R4 — Rate limits de Rspamd. (Medio)**
Un usuario con `rl_value` bajo verá envíos rechazados. **Mitigación:** leer el límite por API y mostrarlo en la UI; encolar con backoff.

**R5 — Envío y APPEND a Sent no son atómicos. (Medio)**
Ver §4.2. **Mitigación:** outbox transaccional; nunca reintentar SMTP tras 250.

**R6 — Cambios upstream de Mailcow. (Medio)**
Precedente concreto: Solr fue **eliminado** en Jan 2025 y las variables `SOLR_*` borradas automáticamente de `mailcow.conf`. Nuestra dependencia de `mailcowauth.php` y `passwd-verify.lua` es de *lectura* (entender el comportamiento), no de acoplamiento — pero el nombre de la red, los aliases y los endpoints de la API sí pueden cambiar. **Mitigación:** depender sólo de protocolos estándar (IMAP/SMTP/Sieve/DAV) para el path crítico; la API sólo para conveniencia, con degradación elegante. Pinnear versión de Mailcow probada y suite de integración contra un Mailcow de test.

**R7 — Nunca tocar el filesystem de vmail. (Crítico si se viola)**
`maildir_very_dirty_syncs = yes` hace que Dovecot confíe en sus índices. Escribir en `cur/` por fuera de Dovecot **corrompe buzones**. **Mitigación:** regla de arquitectura explícita; ningún volumen de vmail montado en nuestro contenedor.

**R8 — Concurrencia con SOGo/Thunderbird. (Bajo-medio)**
IMAP está diseñado para multi-cliente, pero duplicados en Sent y estados de flags divergentes son reales. **Mitigación:** dedupe por `Message-ID`; confiar en CONDSTORE para reconciliar flags.

**R9 — Presión de conexiones IMAP con IDLE. (Medio, escala)**
`process_limit = 10000` en `imap-login` y `service_count = 1` (un proceso por conexión). Con N usuarios × M carpetas en IDLE, el número explota. **Mitigación:** IDLE sólo en INBOX + carpetas realmente abiertas; poll con QRESYNC para el resto; pool compartido con timeout agresivo. Medir en piloto.

**R10 — `SKIP_FTS` puede estar en `y`. (Medio)**
Los defaults difieren entre `docker-compose.yml` (`y`) y la doc (`n`). Con FTS apagado, `SEARCH BODY` escanea todo el buzón. **Mitigación:** detectar la capability al conectar y degradar la UI.

---

## 9. Nota sobre nuestra instalación

Verificaciones pendientes cuando haya acceso autorizado al servidor (**no realizadas en este research — sin SSH**):

1. **`CLAUDE.md` dice "Solr habilitado"** — Solr fue eliminado de Mailcow en enero de 2025. Nuestro `2026-03b` ya no lo tiene. Confirmar `SKIP_FTS` real y **corregir la doc del repo**.
2. Confirmar `COMPOSE_PROJECT_NAME` para fijar el nombre exacto de la red.
3. Confirmar si hay `DOVECOT_MASTER_USER` estático definido.
4. Confirmar `IPV4_NETWORK` (default `172.22.1`).
5. Revisar capacidad para IDLE con los 89 buzones de Crash (584 GB) — caso de estrés de R9.

---

## 10. Fuentes

**Documentación oficial de Mailcow**
- https://docs.mailcow.email/
- https://docs.mailcow.email/manual-guides/Dovecot/u_e-dovecot-static_master/
- https://docs.mailcow.email/manual-guides/Dovecot/u_e-dovecot-fts/
- https://docs.mailcow.email/manual-guides/Dovecot/u_e-dovecot-performance/
- https://docs.mailcow.email/manual-guides/mailcow-UI/u_e-mailcow_ui-keycloak/
- https://docs.mailcow.email/manual-guides/SOGo/u_e-sogo/
- https://docs.mailcow.email/third_party/mailman3/third_party-mailman3/
- https://docs.mailcow.email/post_installation/firststeps-rp/

**Código fuente (mailcow/mailcow-dockerized, branch master)**
- `docker-compose.yml` — https://github.com/mailcow/mailcow-dockerized/blob/master/docker-compose.yml
- `data/conf/dovecot/dovecot.conf`
- `data/conf/dovecot/dovecot.folders.conf`
- `data/conf/dovecot/auth/passwd-verify.lua`
- `data/conf/dovecot/auth/mailcowauth.php`
- `data/Dockerfiles/dovecot/docker-entrypoint.sh`
- `data/web/inc/functions.auth.inc.php`
- `data/web/api/openapi.yaml`
- `data/conf/nginx/templates/sites-default.conf.j2`
- `data/conf/rspamd/lua/ratelimit.lua`

**Blog de Mailcow**
- https://mailcow.email/posts/2023/mailcow-idp/
- https://mailcow.email/posts/2025/release-2025-01/ (Solr → Flatcurve)
- https://mailcow.email/posts/2024/release-2024-06/
- https://mailcow.email/posts/2025/nightly-progress/
- https://github.com/mailcow/mailcow-dockerized/releases/2025-01
- https://github.com/mailcow/mailcow-dockerized/pull/4456

**Dovecot / Rspamd**
- https://doc.dovecot.org/main/core/plugins/fts_flatcurve.html
- https://doc.dovecotpro.com/main/core/config/auth/master_users.html
- https://rspamd.com/doc/modules/ratelimit.html

**Referencia secundaria**
- https://deepwiki.com/mailcow/mailcow-dockerized/5.5-identity-provider-integration
- https://deepwiki.com/mailcow/mailcow-dockerized/4-user-mail-access
