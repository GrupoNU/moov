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

**Apagado por defecto** (`MOOV_ENABLED=false`): hoy el endpoint responde 404, igual que
hace moovd con la sesión delegada en un host sin emisor configurado. **Nada desplegado.**

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
3. **Cuándo quieren que encendamos `MOOV_ENABLED`.** Mientras esté en `false` nuestro
   JWKS responde 404, así que no pueden probar contra él aunque configuren el issuer.
   Nos avisan y lo encendemos; es una variable de entorno.

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
| Emisor + JWKS | ✅ construido, 37 tests, sin desplegar |
| Tarjeta del portal | pendiente (esperando definiciones de producto de Diego) |
| Cliente de la API de cuentas | siguiente, contra el simulador de Prism |
| Retención (90/166/180 días) | pendiente |
| Prueba contra emisor real | ⏳ **bloqueada en §4**: necesitamos el host piloto |
