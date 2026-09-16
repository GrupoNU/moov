# Nota de VPS_Mail al equipo Moov — cambio de dominio (D-1) y contrato de errores de Mailcow

> **De:** sesión VPS_Mail · **Para:** sesión Moov · **Fecha:** 2026-09-15
> **Contexto:** F0 del L2 `docs/director/L2-integracion-moov-corppass.md` (repo del grupo)
>
> **Lean primero §5:** responde las cuatro preguntas abiertas de su
> `docs/specs/L2-accounts-api-contract.md` §7. Una de ellas (P3, `smtp_access`) **corrige un
> supuesto del contrato** y afecta cómo implementar el estado `readOnly` de §2.5.
>
> **Acción requerida:** revisar §3 (contrato de errores, cambia el cliente Mailcow de M1),
> §4 (detalles de la API) y §5 P3 (decisión sobre solo-lectura). El cambio de dominio (§1)
> es informativo.
>
> **Actualización 2026-09-16 — §6 y §7:** el DNS de `corppass.events` ya está publicado y
> verificado, el dominio recibe correo y **pueden apuntar las pruebas de M1 ahí**. La P2
> quedó cerrada (el DELETE sí borra el maildir). **§7 es lo único que les pedimos:** sumar
> `mail.corppass.events` a `Caddyfile.public` (dos líneas, sin tocar rutas), con el aviso
> de por qué el A apunta a IP-B y no a IP-A.

---

## 1. Cambio de dominio (D-1) — informativo

Diego decidió el **2026-09-15** que las casillas de evento van en:

```
<slug>@corppass.events          (antes: <slug>@eventos.corppass.app)
```

El host de la PWA pasa a ser `mail.corppass.events`.

**Por qué casi no les afecta:** el brief de Moov trata el dominio como configuración
(`moovctl service-account create -domain <d>`) y no hay ninguna referencia al dominio viejo
en `docs/` de Moov (verificado). El cambio es de valor, no de contrato. Donde sí impacta es
en lo que se le pase a `-domain` y a la allow-list del cliente Mailcow al desplegar.

El L2 ya fue actualizado (D-1 reescrita, D-1.1 nueva con las consecuencias, §4.5 ampliada).

**Contexto que conviene que conozcan:** `corppass.events` es un **dominio raíz**, no un
subdominio de `corppass.app`. No hereda reputación (el 9.4/10 de `corppass.app` no aplica):
zona DNS nueva, DKIM nuevo, reputación desde cero y penalización temporal de dominio nuevo
(~28 días). VPS_Mail hará warming antes del primer evento de volumen. Para el gate F5,
criterio 3 (entrega a Gmail/Outlook/Yahoo con SPF/DKIM/DMARC en pass), esto significa que
conviene **no programar esa prueba contra un dominio recién dado de alta sin warming
previo** — el resultado no sería representativo.

---

## 2. ✅ Acceso a Mailcow: ya resuelto, no necesitan nada nuevo

**moovd ya está conectado a `mailcowdockerized_mailcow-network`** (IP `172.22.1.15`), y desde
esa red la API de Mailcow responde en **`https://172.22.1.11`** con la clave read-write
existente. Es exactamente la topología del L2 §4.3 y ya está en su lugar.

Para M1:

- Endpoint: `https://172.22.1.11` (o el nombre de servicio del nginx de Mailcow), **TLS sin
  verificar** — es certificado interno.
- Credencial: la clave read-write que ya tenemos inventariada. No hace falta crear ninguna.
- No hay que cambiar redes Docker, ni la allow-list, ni `mailcow.conf`.

Sobre `MOOV_MAILCOW_TEST_*` de su brief: hoy **no existe** ninguna de esas variables en el
entorno. Cuando quieran correr el test de integración contra Mailcow real, nos avisan y
coordinamos cómo se la inyectamos a moovd (la clave no debe quedar escrita en el repo).

---

## 3. ⚠️ Contrato de errores de Mailcow — esto sí cambia el código de M1

Verificado empíricamente contra nuestra instalación (Mailcow 2026-07a). **Prácticamente
todos los fallos devuelven HTTP 200.**

| Caso | HTTP | Cuerpo |
|---|---|---|
| Lectura, sin cabecera | 200 | *(vacío)* |
| Lectura, clave inválida | **200** | `{"type":"error","msg":"authentication failed"}` |
| Escritura, clave inválida | **401** | `{"type":"error","msg":"authentication failed"}` |
| Clave válida, IP no autorizada | 200 | `{"type":"error","msg":"api access denied for ip <IP>"}` |
| Buzón duplicado | **200** | `[{"type":"danger",…,"msg":["object_exists","dir@dominio"]}]` |
| Dominio ajeno / inexistente | **200** | `[{"type":"danger",…,"msg":"access_denied"}]` |
| Borrar buzón inexistente | **200** | `[{"type":"danger",…,"msg":"access_denied"}]` |
| Contraseña débil | **200** | `[{"type":"danger","log":["password_check",null,null],"msg":"password_complexity"}]` |
| Contraseñas no coinciden | **200** | `[{"type":"danger",…,"msg":"password_mismatch"}]` |
| Cuota sobre el máximo | **200** | `[{"type":"danger",…,"msg":["mailbox_quota_exceeded",2048]}]` |
| `items` mal formado | **200** | `[{"type":"danger",…,"msg":["username_invalid",[…]]}]` |
| Entidad inexistente (GET) | **200** | `{}` |

### Reglas para el cliente de `internal/mailcow`

1. **Nunca tratar `HTTP 200` como éxito.**
2. **Hay DOS familias de error, no una:**
   - `type: "error"` → transporte/autenticación (objeto único).
   - `type: "danger"` → **fallos de operación** (dentro del array de resultados).
   Chequear solo `"error"` —que es lo que sugería nuestra nota preliminar— **deja pasar todos
   los fallos de negocio**. Hay que comprobar ambas.
3. **`msg` es string o array según el caso** (`"access_denied"` vs
   `["object_exists","dir@dominio"]`). Normalizar antes de comparar.
4. **La respuesta de escritura es un array**: recorrer todas las entradas, no solo la primera.
   Una sola llamada puede producir varias (crear un dominio devuelve rate limit + DKIM + alta).
5. **Cuerpo vacío = fallo** (petición sin autenticación), no "sin resultados".
6. ⚠️ **`{}` en un GET significa "no existe" — y es indistinguible de un fallo de
   autenticación silencioso.** Esto es **crítico para su idempotencia por `address`**: un GET
   que devuelve `{}` porque la clave es mala se leería como "el buzón no existe" y llevaría a
   crear duplicados. **Validen la credencial al arrancar**, no la infieran de un `{}`.
7. **`access_denied` no siempre es permisos**: también es la respuesta a "la entidad no
   existe". No lo escalen como fallo de credencial.
8. **La API no se autolimita.** 30 peticiones seguidas → las 30 con 200, sin throttling ni
   429. El límite de tasa lo tienen que poner ustedes.

Sugerencia: que el fake de tests reproduzca estos casos, no solo el camino feliz. Es
exactamente lo que un fake "razonable" no adivina.

---

## 4. Detalles de la API que les van a ahorrar tiempo

Todos verificados con cuerpos reales (informe completo en el enlace del final).

- **`edit/mailbox` exige `items` como array plano.** La forma `{"items":{"anyOf":[…]}}` que
  aparece en algunos ejemplos **falla** con `username_invalid`. Correcto:
  `{"items":["dir@dominio"],"attr":{…}}`.
- **`delete/mailbox` NO usa envoltorio**: el cuerpo es un array plano de direcciones,
  `["dir@dominio"]`.
- **Suspender no tiene endpoint propio**: es `edit/mailbox` con `attr:{"active":0}`.
- ⚠️ **Solo lectura: `attr:{"smtp_access":0}` se guarda pero NO bloquea el envío.** Ver P3
  en §5 — es el hallazgo que más les afecta.
- **Rate limit por buzón: `POST /edit/rl-mbox`** con `{"rl_value":300,"rl_frame":"d"}`
  (`s`/`m`/`h`/`d`). Los 300/día de D-3 ya están fijados **en el dominio**, así que cada
  casilla nueva los hereda (`rl_scope: "domain"` en el GET) — solo hace falta fijarlo por
  buzón si quieren un valor distinto.
- **App passwords admiten protocolos granulares**: `"protocols":["imap_access","smtp_access"]`
  deja fuera DAV/EAS/POP3/Sieve. Recomendado para reducir superficie.
- ⚠️ **El listado de app passwords devuelve el hash bcrypt, no la contraseña.** Moov **debe
  persistir la que generó** al crearla; no hay forma de recuperarla después.
- **`quota` va en MB al escribir y vuelve en bytes al leer.**
- **El GET de buzón ya trae lo que necesita su `GET /admin/accounts/{address}`**:
  `quota_used`, `percent_in_use`, `last_imap_login`, `last_smtp_login`, `active`.
- **El DKIM 2048 se genera solo** al crear el dominio; no hay que pedirlo aparte.

Mapeo sugerido a su `{field, reason}`: `object_exists`→`address` (respuesta idempotente),
`password_complexity`/`password_mismatch`→`password`, `mailbox_quota_exceeded`→`quotaMB`
(el máximo viene en el array), `username_invalid`/`access_denied`→`address`.

---

## 5. Respuestas a sus cuatro preguntas abiertas (§7 del contrato)

Vimos que `docs/specs/L2-accounts-api-contract.md` §7 dejó cuatro puntos para F0. Acá van,
con evidencia.

### P1 — ¿Mailcow acepta un frame por día (`rl_frame: "d"`)? → **SÍ**

`POST /edit/rl-mbox` con `{"items":["dir@dominio"],"attr":{"rl_value":300,"rl_frame":"d"}}`
→ `{"type":"success",…,"msg":["rl_saved","dir@dominio"]}`, y `GET /get/rl-mbox/{address}`
devuelve `{"value":"300","frame":"d"}`.

Frames disponibles: `s` / `m` / `h` / `d`. **`sendPerDay` puede apoyarse en Mailcow**; no
hace falta el plan B de aplicarlo en el outbox. Además ya está fijado **a nivel dominio** en
`corppass.events`, así que cada casilla nueva lo hereda (`rl_scope: "domain"`); solo hace
falta escribirlo por buzón si quieren un valor distinto del heredado.

### P2 — ¿El DELETE borra el maildir de forma síncrona? → **SÍ** *(cerrado 2026-09-16)*

Verificado con entrega real. Un buzón que **sí recibió correo** (mensaje entregado por LMTP
interno; `"messages": 1`, `"quota_used": 427`, y en disco 6 archivos / 32K en
`/var/vmail/corppass.events/maildir-test`):

```
POST /delete/mailbox ["maildir-test@corppass.events"]  →  msg:["mailbox_removed", …]
ls -d /var/vmail/corppass.events/maildir-test          →  ELIMINADO (inmediatamente después)
```

El directorio del dominio queda vacío y la API reporta cero buzones. **No hay ventana de
limpieza diferida**: al momento de responder la API, el contenido ya no está en disco. Su
estado `deleting` no necesita esperar a Mailcow — el tiempo que dure es el de la purga de
caché y blobs del lado de Moov.

Las app passwords también se borran en cascada (listado vacío, sin huérfanas).

Con esto el **criterio 7 del gate F5** queda cerrado en su parte de Mailcow.

### P3 — ⚠️ ¿`smtp_access:0` rechaza el AUTH de submission dejando IMAP intacto? → **NO**

**Esto corrige lo que les dijimos en la versión anterior de esta nota.** Medido contra un
buzón de prueba real:

```
SMTPS 465 (submission), smtp_access=1  → 235 2.7.0 Authentication successful
SMTPS 465 (submission), smtp_access=0  → 235 2.7.0 Authentication successful   ← sigue pasando
```

El atributo se guarda bien (`"smtp_access":"0"`, `"imap_access":"1"` en el GET), pero no se
aplica en el camino de submission. La razón está en el código:

- `/web/inc/functions.auth.inc.php` chequea `<service>_access` **solo si el cliente reporta
  un `service`**: `if ($extra['service'] != 'NONE') { $key = strtolower($extra['service']) . "_access"; … }`.
  Cuando el servicio llega como `NONE`, el chequeo se saltea entero.
- Del lado de Postfix no hay red de contención: `submission/smtps · smtpd_client_restrictions
  = permit_mynetworks, permit_sasl_authenticated, reject` (solo exige estar autenticado), y
  **ningún mapa SQL de Postfix consulta `smtp_access`** (verificado sobre
  `/opt/postfix/conf/sql/`).

**Consecuencia para su §2.5 y para M1:** `smtp_access:0` **no sirve como único mecanismo de
solo lectura**. Opciones:

| Opción | Dónde | Comentario |
|---|---|---|
| Bloquear `EmailSubmission/set` | Moov | Ya está en su brief. **Suficiente mientras Moov sea la única puerta**, que es el principio del L2 |
| **Reemitir la app password sin SMTP** al pasar a read-only | Moov | Cierra la puerta de verdad, también para un cliente externo. Los protocolos son granulares: `"protocols":["imap_access"]` |
| `rl_value: 0` | Mailcow | Bloquearía a nivel MTA. **No verificado** — lo probamos en F3 |

**Sugerencia nuestra:** las dos primeras combinadas. Moov bloquea en la interfaz (mensaje
claro al organizador) **y** reemite la app password sin SMTP (defensa real). Así el estado
`readOnly` de su contrato no depende de un atributo que no se aplica.

### P4 — ¿Mailcow respeta la allow-list de IP de las claves? → **SÍ, estrictamente**

Con clave válida desde una IP no autorizada:
`{"type":"error","msg":"api access denied for ip 217.216.83.79"}`.

Es un error **distinto** del de credencial (`authentication failed`), y **revela la IP de
origen** que ve Mailcow — la forma práctica de saber qué autorizar. Trátenlos por separado:
la causa y la remediación no son la misma.

---

## 6. Estado del dominio — **operativo para recibir** *(actualizado 2026-09-16)*

`corppass.events` está dado de alta en Mailcow con los límites de D-3 (2 GB por buzón,
300 envíos/día heredados del dominio) y **el DNS está publicado y verificado**:

| Registro | Valor | Estado |
|---|---|---|
| MX | `mail.atmosfera.cloud` (prio 10) | ✅ |
| SPF | `v=spf1 mx ip4:217.216.83.79 -all` | ✅ |
| DKIM | RSA 2048, selector `dkim` | ✅ |
| DMARC | `p=quarantine; adkim=s; aspf=s` | ✅ |
| `mail.corppass.events` | A → **217.216.85.211** | ✅ |

Recepción verificada contra los mapas de Postfix: el dominio es local, un buzón resuelve a su
maildir y un destinatario inexistente se rechaza. **Pueden apuntar sus pruebas de M1 a este
dominio ya.**

Existe `warmup@corppass.events` (512 MB), creado para el warming. No lo borren.

---

## 7. ⚠️ Lo que necesitamos de ustedes: el host en Caddy

`Caddyfile.public` es de ustedes (`/opt/moov/src/deploy/`), así que **no lo tocamos** —
CLAUDE.md nos prohíbe editar el scope de otro equipo. El cambio es de dos líneas: agregar
`mail.corppass.events` a los dos bloques de host.

```
# bloque :80
http://moov.atmosfera.cloud:80, …, http://mail.corppass.events:80 {

# bloque :443
https://moov.atmosfera.cloud:443, …, https://mail.corppass.events:443 {
```

**No hace falta tocar rutas**: su matcher `@jmap` ya incluye `/admin/*` y `/auth/delegated*`,
así que M1 y M2 quedan enrutados solos.

### Por qué el A apunta a IP-B y no a IP-A

Es el punto que más conviene que verifiquen antes de recargar. `moov-caddy-public` está
publicado por Docker **exclusivamente** en `217.216.85.211:80/443`; IP-A es de Mailcow y su
nginx tiene tomados esos puertos. Por eso el registro A de `mail.corppass.events` apunta a
IP-B: **con IP-A el challenge HTTP-01 fallaría y el host se quedaría sin certificado.**
Su propio `Caddyfile.public` ya documenta esta separación; esto solo lo confirma en campo.

El DNS ya está publicado, así que el challenge tiene todo lo que necesita. Al recargar:

```bash
docker exec moov-caddy-public caddy reload --config /etc/caddy/Caddyfile
docker logs moov-caddy-public --tail 30 | grep -i "certificate obtained\|mail.corppass.events"
```

Si prefieren que lo agreguemos nosotros, dígannos y lo hacemos — pero por defecto lo dejamos
en sus manos.

### Marca CorpPass

Preparamos el `branding.json` con el color canónico de marca (`#06b6d4`, de
`CORPPASS_BRAND_TOKENS.md`) siguiendo el formato de `mail.areacorp.com.ar`. **Faltan los
assets** (`logo.png`, `logo-dark.png`, `icon.png`); los pedimos al equipo de marca. Lo
aplicamos con `moovctl branding set` cuando los tengamos.

---

Informe completo: `D:\git\VPS_Mail\docs\spikes\2026-09-15-mailcow-api-moov-corppass.md`
Runbook de F3: `D:\git\VPS_Mail\docs\runbooks\f3-corppass-events-publicacion.md`
