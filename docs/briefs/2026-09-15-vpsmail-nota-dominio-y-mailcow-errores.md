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

### P2 — ¿El DELETE borra el maildir de forma síncrona? → **PARCIAL, sin confirmar**

La respuesta de `POST /delete/mailbox` es inmediata y síncrona
(`msg:["mailbox_removed", …]`), el `GET` posterior devuelve `{}` y **las app passwords se
borran en cascada** (verificado: el listado queda vacío, sin huérfanas).

Lo que **no** pudimos confirmar es el borrado del maildir en disco: el buzón de prueba nunca
recibió correo y Dovecot crea `/var/vmail/<dominio>/<local>` recién con el primer mensaje.
Lo cerramos en F3/F5 con una casilla que haya recibido algo. Hasta entonces **no asuman que
`deleting` es instantáneo** para el criterio 7 del gate.

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

## 6. Estado del dominio

`corppass.events` **ya está dado de alta en Mailcow** (2026-09-15) con los límites de D-3
(2 GB por buzón, 300 envíos/día heredados) y DKIM 2048 generado. **Todavía sin DNS
publicado**, así que no recibe ni envía: eso lo hace VPS_Mail en F3.

Pueden apuntar sus pruebas de M1 a ese dominio cuando lo necesiten.

Una sola cosa **no** quedó verificada y la heredamos a F3/F5: que el borrado elimine un
**maildir con contenido real** (el buzón de prueba nunca recibió correo, y Dovecot crea el
maildir recién con el primer mensaje). Lo confirmamos antes del gate F5, criterio 7.

Informe completo: `D:\git\VPS_Mail\docs\spikes\2026-09-15-mailcow-api-moov-corppass.md`
