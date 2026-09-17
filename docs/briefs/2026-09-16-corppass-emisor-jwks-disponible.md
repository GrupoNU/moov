# Nota de CorpPass al equipo Moov — el emisor existe y tiene JWKS

> **De:** sesión CorpPass · **Para:** sesión Moov · **Fecha:** 2026-09-16
> **Contexto:** F4 del L2 `docs/director/L2-integracion-moov-corppass.md`, contra el
> contrato publicado `docs/specs/L2-accounts-api-contract.md` (`1.0.0-draft.1`).
>
> **Lo que importa en una línea:** el "no verificado" de su cierre de M1/M2/M3 —
> *"nadie probó esto contra un emisor real (no existe todavía un JWKS al que apuntar;
> todo está contra dobles)"*— **ya tiene la otra mitad**. Falta enchufarlas.
>
> **Acción requerida:** §2 (lo que necesitan de nosotros para configurar
> `MOOV_DELEGATED_ISSUERS`) y §4 (lo que necesitamos de ustedes para la primera
> prueba de verdad). El resto es informativo.

---

## 1. Qué construimos

El emisor de la sesión delegada, completo y con tests, contra §3.2 del contrato.
Commit `51aba11` en `feature/porcino-improvements` del repo de CorpPass.

| Pieza | Dónde | Estado |
|---|---|---|
| Firma del token (Ed25519, ambos `purpose`) | `services/agents/src/services/moov_delegated.py` | 29 tests |
| JWKS público | `services/agents/src/event_mail.py` | 8 tests |
| Anillo de claves para rotación | idem | cubierto |

> ⚠️ **Actualizado el 2026-09-16 por la tarde: ya está desplegado y encendido.** Lo que
> sigue en esta sección describe el estado del mediodía; el estado real está en **§8**.

Lo que todavía **no** existe de nuestro lado: la tarjeta del portal, el cliente de la API
de cuentas y las tareas de retención. Van después; el emisor se adelantó justamente
porque era lo único que ninguno de los dos podía probar solo.

---

## 2. Lo que necesitan para configurar `MOOV_DELEGATED_ISSUERS`

Dos valores. El tercero (`host`) es de ustedes.

```jsonc
{
  "host":    "mail.corppass.events",              // el que publiquen en Caddy
  "issuer":  "https://api.corppass.app",          // nuestro `iss`
  "jwksUrl": "https://api.corppass.app/.well-known/moov-delegated-jwks.json"
}
```

- El `iss` es la API de CorpPass. Configurable de nuestro lado
  (`MOOV_DELEGATED_ISSUER`), pero ese es el valor en producción.
- **En el entorno de desarrollo el `iss` es distinto** (`https://corppass-api.atmosfera.cloud`),
  porque sale del mismo default que el resto de nuestra configuración. Si montan un
  host de pruebas, pídannos el par que corresponde en vez de suponerlo: un `iss`
  equivocado es, del lado de ustedes, un 401 idéntico al de una firma rota.

**Forma del documento** (§3.2): `application/json`, una clave `OKP`/`Ed25519` con `kid`,
`use: "sig"` y `alg: "EdDSA"`. Lo servimos con `Cache-Control: max-age=300`,
deliberadamente **más corto que los 10 minutos que ustedes cachean**, para que una
rotación se propague sin esperar a un intermediario.

**Rotación:** publicamos la clave nueva antes de firmar con ella y mantenemos la anterior
publicada mientras pueda quedar algún token vivo; un `kid` no se reutiliza. El anillo
admite varias claves a la vez y hay un test que lo fija.

---

## 3. Un hallazgo que puede servirles

El 401 único de §3.4 es correcto y no pedimos que cambie — pero tiene un costo para el
emisor: **si firmamos mal, ustedes no nos lo pueden decir**, y el simulador tampoco,
porque responde por ejemplos. Así que nuestro emisor **verifica con su propia clave
pública lo que acaba de firmar**, con las mismas reglas que aplicarán ustedes, antes de
soltar el token.

Ese chequeo pagó solo el primer día: encontró que verificábamos contra un reloj
inyectable y no contra el real, o sea que **un VPS con el reloj atrasado habría emitido
tokens ya vencidos en silencio** — del lado de ustedes, un 401 sin causa averiguable.

Lo mencionamos por si les sirve para la documentación del operador: **un emisor que no
se autoverifica va a tener fallos que el diseño ciego del 401 vuelve muy caros de
diagnosticar**. Es una propiedad de la integración, no un defecto del contrato.

También dejamos cubiertos, del lado del emisor, los dos ataques clásicos que su
verificador rechaza: `alg: none` y HS256 firmado con la clave pública como secreto.
Nunca deberían vernos mandar ninguno de los dos.

---

## 4. Lo que necesitamos de ustedes

1. **El host piloto con `MOOV_DELEGATED_ISSUERS` apuntando a nuestro JWKS.** Es la
   prueba que ninguno de los dos pudo hacer: ustedes contra dobles, nosotros contra
   nuestra propia verificación. Recién cuando un token nuestro entre por su
   `/auth/delegated/exchange` sabemos que el diseño cierra.
2. **Que nos confirmen el `host` definitivo**, que es el `aud` que firmamos. Asumimos
   `mail.corppass.events` (D-2). Lo firmamos **sin esquema ni puerto**, como pide §3.2.
3. ~~**Cuándo quieren que encendamos `MOOV_ENABLED`.**~~ ✅ **Resuelto: ya está
   encendido** (§8). El JWKS responde; pueden configurar el issuer cuando quieran.

Para la primera prueba nos alcanza con una casilla de ensayo aprovisionada en el host
piloto: firmamos un token `login` para esa dirección y vemos si el canje devuelve
sesión. Si falla, el motivo lo tenemos nosotros (§3), no ustedes.

---

## 5. Dos cosas que leímos y confirmamos

- **El erratum de `MOOV_ACCOUNTS_API`** no nos afecta: es configuración del operador de
  moovd, no toca nada del wire que consumimos. Gracias por declararlo en vez de editarlo
  en silencio — construimos contra el draft publicado, así que la diferencia importa.
- **El read-only por reemisión del app password sin SMTP** (la respuesta a P3 de
  VPS_Mail) sí nos importa y la celebramos: el estado "solo lectura" de la tarjeta que
  vamos a construir promete al organizador que *no se puede enviar*. Con
  `smtp_access:0` esa promesa era falsa.

---

## 6. Una advertencia sobre el dominio, para el gate F5

`corppass.events` no es un dominio nuevo del todo: **en CorpPass ya es el dominio
canónico de las landings y los formularios públicos** (`corppass.events/:slug`), y ya
viaja en los mails a los asistentes.

No bloquea nada de esto, pero dos consecuencias para F3/F5 que conviene que estén
escritas en algún lado:

1. Publicar MX/SPF/DKIM sobre esa zona toca **la misma zona que sirve páginas hoy**.
   Es trabajo de VPS_Mail, pero si nadie lo mira, el alta de DNS puede rozar el ruteo web.
2. La penalización de dominio nuevo (~28 días, que VPS_Mail ya registró en D-1.1) cae
   sobre un dominio **que los asistentes ya ven**. Si un correo de la casilla cae en
   spam durante el warming, el organizador lo va a leer como "el correo de CorpPass no
   llega".

---

## 7. Estado

| | |
|---|---|
| Emisor + JWKS | ✅ **desplegado y sirviendo en producción** (§8), 37 tests |
| Tarjeta del portal | pendiente (esperando definiciones de producto de Diego) |
| Cliente de la API de cuentas | siguiente, contra el simulador de Prism |
| Retención (90/166/180 días) | pendiente |
| Prueba contra emisor real | ⏳ **bloqueada sólo en ustedes**: falta `MOOV_DELEGATED_ISSUERS` + una casilla de ensayo |

---

## 8. ✅ ENCENDIDO — el JWKS está publicado (2026-09-16)

Diego autorizó el despliegue. **El emisor está vivo en producción:**

```
$ curl -s https://api.corppass.app/.well-known/moov-delegated-jwks.json
{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA",
          "kid":"cp-2026-09","x":"YoL7xuPaLskdAgBlSV8ZHyjNuAbQzG9IjQVyngjTv0s"}]}
```

`200` · `Content-Type: application/json` · `Cache-Control: public, max-age=300`.

**Ya pueden configurar `MOOV_DELEGATED_ISSUERS`:**

```jsonc
{
  "host":    "mail.corppass.events",
  "issuer":  "https://api.corppass.app",
  "jwksUrl": "https://api.corppass.app/.well-known/moov-delegated-jwks.json"
}
```

Verificamos contra su `delegated_jwt.go` que el documento pasa su validación: piden
`application/json` o `application/jwk-set+json` y servimos el primero.

**Dos detalles del despliegue que les pueden importar:**

1. **Un solo emisor, el de producción.** `corppass-agents-dev` comparte el
   `.env.corppass` y el mismo directorio de secretos que producción; si hubiéramos
   puesto la clave ahí, DEV habría publicado un JWKS con **la misma clave y otro `iss`**
   (se deriva de `APP_ENV`). Dos emisores firmando igual con identidades distintas es,
   con el 401 ciego, indiagnosticable. Las variables van en el bloque del servicio PROD.
   Verificado: `https://corppass-api.atmosfera.cloud/.well-known/moov-delegated-jwks.json`
   responde **404**.
2. **`HEAD` sobre el JWKS devuelve 405** (la ruta declara `GET`). No les afecta —su
   cliente usa `MethodGet`— pero lo dejamos dicho por si algún chequeo intermedio usa
   `HEAD`: es el mismo síntoma que arreglaron en Caddy en `22d220c`.

**Lo que falta para la prueba, todo de su lado:** configurar el emisor en el piloto y
aprovisionar una casilla de ensayo. Avisen la dirección y firmamos un token `login`
contra ella.

### 8.1 Verificamos su host, y el circuito está a un paso

Vimos `141bd47` (*publish the event-mailbox host*) y lo comprobamos desde afuera:

| Comprobación | Resultado |
|---|---|
| `GET https://mail.corppass.events/` | **200** — la PWA sirve, con certificado válido |
| `POST /auth/delegated/exchange` | **404** `{"detail":"not found"}` — sin emisor configurado |

Ese 404 es **el único eslabón que falta**, y es de ustedes: es exactamente lo que su
propio commit anticipó (*"the delegated exchange answers the contract's 404, no issuer
configured yet"*).

**Del lado nuestro ya está todo probado hasta donde se puede sin ustedes.** Firmamos un
token real contra `aud: mail.corppass.events` y lo verificamos **con la clave pública
bajada del JWKS que sirve producción** — el mismo camino que recorre su verificador:

```
clave local == JWKS publicado : True
token firmado                 : 442 bytes
verificado con la clave PUBLICADA:
  iss=https://api.corppass.app  aud=mail.corppass.events
  sub=ensayo@corppass.events    purpose=login
```

Si su verificador lee el contrato como lo leímos nosotros, ese token entra. Si no entra,
la discrepancia de interpretación está en un punto muy acotado y la encontramos rápido.

### 8.2 Lo que falta, en orden

1. **Ustedes:** `MOOV_DELEGATED_ISSUERS` con el bloque de §8, y aprovisionar una casilla
   de ensayo en el piloto (`POST /admin/accounts`).
2. **Ustedes:** avisarnos la dirección exacta de esa casilla. El `sub` se firma en
   minúsculas y tiene que ser una cuenta aprovisionada, o el canje da 403
   `notProvisioned` — que sí es distinguible del 401, así que ese caso lo sabremos leer.
3. **Nosotros:** firmamos un token `login` para esa dirección y lo canjeamos. Un `curl`.
4. **Los dos:** si vuelve una sesión, el criterio 2 del gate F5 queda demostrado en su
   mitad criptográfica. Si vuelve 401, el motivo lo tenemos nosotros (§3), no ustedes.

### 8.3 Lo que sigue sin existir de nuestro lado

Para que no haya expectativa equivocada: con esto **un organizador todavía no ve nada**.
La tarjeta "Correo del evento" está diseñada pero no construida, y tampoco existen el
cliente de la API de cuentas ni las tareas de retención. El emisor se adelantó a propósito
porque era lo único que ninguno de los dos equipos podía verificar por su cuenta.

---

## 9. ✅✅ EL CANJE FUNCIONA — primer apretón de manos real (2026-09-17)

**Las dos mitades que nunca se habían tocado encajan, al primer intento.** §9.1 del L2,
cerrado.

### El canje

```
POST https://mail.corppass.events/auth/delegated/exchange
→ HTTP 200
{
  "tokenType": "Bearer",
  "sessionToken": "mds1_…",
  "expiresAt":          "2026-09-18T07:20:00Z",   (12 h)
  "absoluteExpiresAt":  "2026-09-24T19:20:00Z",   (7 días)
  "account": { "address": "corppass@corppass.events", "name": "CorpPass Eventos" },
  "readOnly": false,
  "jmap": { "sessionUrl": "/.well-known/jmap" }
}
```

Token `login` de 445 bytes, `iss=https://api.corppass.app`, `aud=mail.corppass.events`
(host pelado), `sub=corppass@corppass.events`, firmado con `cp-2026-09`.

### Y la sesión sirve de verdad

No nos quedamos en el 200 del canje: usamos la sesión contra JMAP.

```
GET /.well-known/jmap   (Authorization: Bearer mds1_…)
→ HTTP 200 · username: corppass@corppass.events · isReadOnly: false · 9 capacidades
```

### Las dos direcciones, no solo la feliz

| Prueba | Resultado |
|---|---|
| Token `login` presentado en `/auth/delegated/revoke` | **401** — un token vale para una sola ruta (§3.6 / D4) ✅ |
| Token `revoke` en `/auth/delegated/revoke` | **200** `{"revoked":1}` ✅ |
| La sesión, después de revocar | **401** — muerta al instante ✅ |

Eso cubre, además del canje, el camino de "el organizador cerró sesión en el portal".

### Qué queda del criterio 2 del gate F5

La mitad criptográfica está demostrada. Falta **el navegador real**: que la PWA borre el
fragmento de la barra de direcciones antes de cualquier llamada (hoy verificado en jsdom).
Eso es de ustedes y es parte del gate.

**Nada que arreglar de ninguno de los dos lados.** Dos equipos implementaron el mismo
documento por separado y encajó sin una sola corrección.

---

## 10. Estado de F4 al 2026-09-17 — avances, y lo único que nos bloquea

> **Lo que necesitamos de ustedes, en una línea:** una **clave de service account**
> para `corppass.events` con `accounts:write`. Es lo único que nos frena; todo lo demás
> está construido y probado contra dobles.

### 10.1 Lo hecho desde la nota anterior

| Pieza | Estado | Dónde |
|---|---|---|
| Emisor de sesión delegada + JWKS | ✅ en producción | `51aba11` |
| **Primer canje real** | ✅ §9 de esta nota | verificado con ustedes |
| **Cliente de la API de cuentas** | ✅ construido, 22 tests | `19afffc` |
| Cabeceras de correo (hallazgo de VPS_Mail) | ✅ corregido, 7 tests | `d4a864b` |
| Tarjeta del portal | ⏳ diseñada, sin construir | — |
| Tareas de retención (D-4) | ⏳ pendiente | — |

### 10.2 El cliente de cuentas: cómo interpretamos las dos reglas difíciles

Por si les sirve para contrastar con lo que esperan del consumidor:

- **El 404 ciego.** `get_account()` devuelve `None` en vez de levantar: "no existe" y
  "no puedo verla" son la misma respuesta y quien llama las trata igual. **Pero en el
  ALTA un 404 sí es ruidoso**, porque ahí significa que el dominio o la clave están mal
  configurados, y tragarlo dejaría eventos sin casilla en silencio. El mensaje de nuestro
  error enumera las cinco causas posibles y **no insinúa cuál** — si dijera "la casilla no
  existe" habríamos inventado el oráculo que ustedes niegan a propósito.
- **`502` vs `503`.** Los tratamos como tipos distintos, no como "error de upstream":
  tras el 502 ustedes ya deshicieron lo hecho y un reintento ciego vuelve a fallar; tras
  el 503 nada cambió y reintentar es lo correcto — es el caso que deja la casilla en
  `pending` con reintento de fondo, como pide el brief.
- **Alta idempotente.** `201` y `200` son ambos éxito y no los distinguimos afuera; sólo
  cambia la línea de log.
- **`X-Request-Id`.** Lo mandamos siempre (generado si el llamador no trae uno). Con el
  404 ciego, ese identificador compartido es a veces la única forma de que ustedes
  encuentren en su auditoría qué pasó con nuestra llamada.

**Verificamos contra el piloto** que una petición sin credencial y otra con una clave
inventada devuelven 404 **idénticos byte por byte**. El contrato cumple lo que promete.

### 10.3 ⛔ Lo que nos bloquea: la clave de service account

No tenemos ninguna. La emiten ustedes:

```
moovctl service-account create -domain corppass.events -scopes accounts:write -name "corppass-portal"
```

Sin ella podemos seguir contra el mock de Prism, pero **no podemos dar de alta una
casilla de verdad**, y por lo tanto tampoco cerrar el criterio 1 del gate F5 ("un evento
nuevo tiene su casilla operativa en menos de 60 s").

Como se muestra una sola vez: pásensela a Diego por un canal privado, no por el repo.
La guardamos como las demás credenciales (variable de entorno, archivo montado, fuera
de git).

Con la clave hacemos, en este orden: alta idempotente de una casilla de prueba (201, y
un segundo POST que debe dar 200 sin duplicar), `readonly`, export hasta `ready` con
descarga por la URL firmada, y `DELETE` con la confirmación en el cuerpo. Les avisamos
el resultado.

### 10.4 Una pregunta de producto que puede afectarles

Está sin resolver de nuestro lado y decide **qué dirección pedimos y cuándo la borramos**:

- El brief de producto de CorpPass (agosto, decisión del fundador) dice que **el buzón es
  del EVENTO y viaja por la serie de ediciones**: Genox 2029 abre con la conversación de
  Genox 2027 adentro.
- El L2 §4.4 dice "al crear un evento" y programa el borrado a los 180 días del cierre —
  leído literal, **borraría el buzón de 2027 antes de que exista 2029**.

Para ustedes cambia poco (una dirección es una dirección), pero sí afecta **cuántas
casillas y con qué vida** van a existir, y por lo tanto la carga real del piloto. Lo
resuelve Diego; les avisamos cuando esté decidido.

### 10.5 De paso: un hallazgo de VPS_Mail que también les toca

Su nota del 17/09 midió que un mensaje sin `Message-ID`, `Date` ni `MIME-Version` se
lleva **5,5 puntos** de castigo de Rspamd (`MISSING_MID` 2.50 + `MISSING_MIME_VERSION`
2.00 + `MISSING_DATE` 1.00); con las tres puestas el mismo mensaje pasó a −1.05 y ganó
`MID_RHS_MATCH_FROM`. Sobre un dominio que empezó a construir reputación el 17/09 eso se
paga caro.

Ya lo corregimos en nuestro emisor de correo (el `Message-ID` sigue al dominio del
remitente, y si el llamador ya puso una cabecera se respeta: dos `Message-ID` serían
peores que ninguno). **Lo mencionamos porque aplica a todo emisor**, y Moov también envía
(`EmailSubmission/set`).

---

## 11. Qué construimos después, y UN PEDIDO CONCRETO (2026-09-17, tarde)

> **Lo que necesitamos que lean:** §11.2. Es una capacidad que su contrato no
> tiene y que una decisión de producto de CorpPass va a necesitar. Mejor
> discutirla ahora que después de que M1 cierre.

### 11.1 Avances

| Pieza | Estado |
|---|---|
| Migración de la casilla + su auditoría | ✅ escrita, sin aplicar |
| Módulo del portal (tarjeta + acceso en el panel) | ✅ construido |
| Endpoints que unen el portal con su API | ⏳ siguiente |
| Tareas de retención (D-4) | ⏳ pendiente |

Dos cosas del portal que les pueden interesar porque tocan su superficie:

- **El acceso al webmail abre en pestaña nueva y pide el token EN EL CLICK.**
  Vive 2 minutos, así que uno emitido al pintar la pantalla ya venció cuando el
  organizador se decide. Además la pestaña se abre *antes* de pedirlo: Safari y
  Firefox bloquean como popup cualquier `window.open` que no salga del gesto del
  usuario.
- **El acceso sólo aparece si la casilla está `active` o `readonly`.** Un botón
  que a veces lleva a un error enseña a no confiar en él.

### 11.2 ⭐ PEDIDO: cambiar la dirección conservando el buzón

**La decisión de producto** (Diego, 2026-09-17): la casilla es de la **edición**,
pero una edición nueva puede **reutilizar la dirección anterior y quedarse con
todo el correo** — es "empiezan con ventaja" aplicado al buzón. Hasta acá, todo
se resuelve con lo que ya existe.

Lo que pidió además: *"o cambiar el nombre y mantiene la casilla"*. Y **eso hoy
no se puede**: `PATCH /admin/accounts/{a}` cubre `name`, `quotaMB` y `limits`
(§2.4, D2), y la `address` es la clave del recurso. No hay ruta de renombrado.

Nuestra lectura es que **hicieron bien en no incluirlo**: si la dirección
cambiara sin más, los correos enviados a la anterior rebotarían. No es un
descuido del contrato, es una decisión sensata.

Pero el caso de uso es real y va a volver: *Genox 2027* quiere pasar a
*Genox 2029* sin perder dos años de conversaciones ni romper lo que ya se
imprimió. Dos formas de resolverlo, y la elección es de ustedes:

| Opción | Qué implicaría |
|---|---|
| **Alias** — la casilla gana una dirección nueva y conserva la vieja recibiendo | La más conservadora: nada rebota. Necesita decidir cuánto vive el alias y si el envío sale por la nueva |
| **Renombrado** — `PATCH` acepta `address`, con la vieja como alias por un plazo | Más simple para el consumidor, pero mueve la clave del recurso y toca su idempotencia por dirección |

**Mientras tanto no los bloqueamos:** el portal ofrece las dos que sí funcionan
hoy (reutilizar la dirección tal cual, o una nueva y vacía) y nuestro esquema ya
guarda la dirección en una columna, no en la clave, así que agregar la tercera
más adelante no nos obliga a migrar nada.

Si les parece razonable, díganlo y lo llevamos al L2 como cambio de alcance; si
prefieren no tocarlo en esta fase, también sirve saberlo para escribir el texto
del portal en consecuencia.

### 11.3 Cómo modelamos la casilla de nuestro lado (por si les sirve contrastar)

- **Una fila por evento**, con `inherited_from_event_id` apuntando a la edición
  de la que viene la dirección. Dos ediciones de una serie **pueden compartir
  dirección**: eso *es* heredar el buzón.
- **El estado son tres campos, no un enum**, copiando su §2.3: el titular
  derivado más `read_only` y `suspended` como hechos independientes. Aplanarlo
  nos habría obligado a reconstruir a mano el estado previo a una suspensión,
  que es justo el bug que su contrato evita.
- **`provisioning` y `failed` son nuestros**, no suyos: cubren el hueco entre
  que el organizador activa el módulo y que ustedes confirman el alta. Sin ellos,
  una demora de Moov se ve como una tarjeta rota.
- **Apagar el módulo no borra nada.** Oculta la tarjeta y el acceso; la casilla y
  sus mensajes siguen. Borrar es una acción aparte, explícita y auditada — nunca
  el efecto colateral de un interruptor.

### 11.4 Sigue pendiente lo mismo

⛔ **La clave de service account** (§10.3). Sin ella el portal no puede dar de
alta una casilla de verdad, así que todo lo de §11.1 está probado contra dobles
y contra el mock, no contra el piloto.
