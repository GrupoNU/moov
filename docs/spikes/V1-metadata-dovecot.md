# V1 — METADATA y keywords en nuestro Dovecot (red de seguridad del arbitraje A6)

**Fecha:** 2026-08-10
**Autor:** agente de implementación E2 (resultados crudos para auditoría del director)
**Estado:** ejecutada completa. **A6 se sostiene, con una corrección importante al límite asumido.**

Validación exigida por la L2 §2.3 como AC explícito de E2. El arbitraje A6 pone
la **asignación** de labels en keywords IMAP y la **definición** (nombre, color,
orden, mapping keyword↔label) en una anotación **IMAP METADATA** privada, para
que ambas sean reconstruibles desde Dovecot y la invariante del ADR-001 —
"Dovecot es la fuente de verdad, Moov es cache reconstruible" — siga valiendo
también para labels.

Eso solo se sostiene si METADATA y las keywords de Maildir aguantan en la
práctica. Esto lo mide.

---

## 0. Entorno

| Ítem | Valor |
|---|---|
| Servidor | Mailcow `mailcowdockerized-dovecot-mailcow-1` |
| Dovecot | 2.3.21.1 |
| Acceso | `dovecot:143` STARTTLS desde `mailcowdockerized_mailcow-network` |
| Cuenta | `moov-test@atmosfera.cloud` (buzón dedicado, sin datos reales) |
| `mail_location` | `maildir:~/` — **Maildir real**, no mdbox |
| `imap_metadata` | **`no` global**, `yes` dentro de `protocol imap` |
| `mail_attribute_dict` | `file:%h/dovecot-attributes` |
| Arnés | `internal/imap/v1probe_test.go` (`MOOV_IMAP_V1_PROBE=1`) |

---

## Resumen de veredictos

| # | Medición | Resultado | Impacto en A6 |
|---|---|---|---|
| V1.1 | Tamaño máximo de anotación | **64 MiB OK, 128 MiB rechazado** | Sin problema: sobra por 3 órdenes de magnitud |
| V1.2 | Persistencia tras reconexión | **Sobrevive** | A6 confirmado |
| V1.3 | `/private/` vs `/shared/` | **Ambos read-write**; server-level también | A6 confirmado; usar `/private/` |
| V1.4 | Techo de keywords (en vivo) | **500+ sin rechazo** | Engañoso — ver V1.5 |
| V1.5 | Techo **durable** de keywords | **26 exactas** | **El límite real de A6. Corrige el supuesto.** |

**Titular:** METADATA es sólido y A6 se sostiene. Pero el techo de keywords
**no es el que devuelve el servidor en vivo**: Dovecot acepta cientos y las
sirve correctamente mientras el índice está caliente, y solo 26 sobreviven en
disco. Un test ingenuo contra el servidor concluye "sin límite" y se equivoca.

---

## V1.1 — Tamaño máximo de anotación: 64 MiB

Escritura + relectura verificando que los bytes vuelven íntegros (aceptar la
escritura no alcanza: un servidor que trunca en silencio es peor que uno que
rechaza).

```
1 KiB … 16 MiB   aceptado y releído íntegro
32 MiB           aceptado, releído 33554432 bytes
64 MiB           aceptado, releído 67108864 bytes
128 MiB          RECHAZADO: unexpected EOF
```

**Lectura para el diseño.** El set completo de definiciones de labels de una
cuenta realista pesa unos pocos KB. Con 64 MiB disponibles el margen es de
más de tres órdenes de magnitud: **el tamaño de la anotación no es una
restricción de diseño** y no hace falta particionar la definición en varias
entradas.

El rechazo a 128 MiB llega como `unexpected EOF` (el servidor corta la
conexión), no como un `NO [TOOBIG]` limpio. Irrelevante en la práctica dada la
distancia al límite, pero anotado: si alguna vez se acerca ese tamaño, el modo
de falla es feo.

## V1.2 — Persistencia: sobrevive

Escritura en una conexión, cierre completo, reconexión nueva, relectura. El
valor vuelve idéntico. La anotación vive en `~/dovecot-attributes` (archivo
plano por cuenta, fuera del message store), lo que además la deja fuera del
camino de cualquier operación sobre mensajes.

**Lectura para el diseño.** La definición de labels es efectivamente
reconstruible desde Dovecot. Es lo que A6 necesitaba y no hace falta el
fallback documentado (definición en DB + export/import, marcado como
no-reconstruible).

## V1.3 — `/private/` vs `/shared/`, y server-level

```
scope /private        read-write, round trip íntegro
scope /shared         read-write, round trip íntegro
server-level (mailbox "")   aceptado
```

Los tres funcionan. **A6 usa `/private/` y se mantiene**, por la razón
correcta: la definición de labels pertenece a la cuenta, no a quien tenga ACL
sobre el buzón. Que `/shared/` también funcione es un dato sobre este servidor,
no una invitación a usarlo.

Que el server-level (nombre de buzón vacío) sea escribible abre una opción que
**no tomamos**: una sola anotación para toda la cuenta en vez de una por buzón.
Se descarta porque `/private/vendor/moov/labels` sobre el buzón raíz ya es
suficiente y el scope de servidor es más ancho que el problema.

⚠️ **`imap_metadata = no` es el default global de Mailcow.** Está en `yes`
únicamente dentro del bloque `protocol imap`, que es el que aplica a clientes
IMAP — o sea, a nosotros. Pero es una config que otra instalación puede no
tener. Es exactamente el riesgo 4 de la L2 §5, ahora con nombre y lugar: **la
guía de instalación de Moov tiene que documentar `imap_metadata = yes` como
requisito**, y el engine debería verificar la capability METADATA al conectar
(ya lo hace: `internal/imap` la exige antes de cualquier operación de
anotación) y degradar con un error claro en vez de perder definiciones.

## V1.4 / V1.5 — El techo de keywords: 500 en vivo, **26 en disco**

Esta es la medición que corrige un supuesto del arbitraje.

### Lo que devuelve el servidor en vivo

Agregando keywords `$MoovL1`, `$MoovL2`, … de a una y releyendo los flags del
mensaje después de cada `STORE`:

```
keyword #50   ok (flags en el mensaje: 50)
keyword #100  ok (100)
…
keyword #500  ok (500)
RESULT keyword-ceiling: 500 aceptadas y persistidas
```

Ningún rechazo, ninguna pérdida silenciosa, hasta 500. Un test que se quede
acá concluye "Maildir no tiene el límite clásico de 26" y **se equivoca**.

### Lo que queda en disco

Con 40 keywords sobre un mensaje, tras un `doveadm force-resync` completo, el
estado real del Maildir es:

```
$ cat .MoovE2Durability/dovecot-keywords     # 26 líneas, índices 0..25
0 $MoovL1
1 $MoovL2
…
25 $MoovL26

$ ls .MoovE2Durability/cur/
1786370770.M706657P1159986...,S=248,W=257:2,abcdefghijklmnopqrstuvwxyz
```

El archivo `dovecot-keywords` corta en el índice **25** (`$MoovL26`) y la
sección de flags del nombre de archivo es exactamente el alfabeto completo:
`abcdefghijklmnopqrstuvwxyz`, **26 letras**.

Las keywords 27 a 500 **no existen en ninguna parte del disco**. Viven solo en
el índice en memoria de Dovecot, que las sirve fielmente mientras está caliente
— por eso la medición en vivo da 500 — y las pierde cuando el índice se
reconstruye desde el Maildir.

Es el límite clásico de Maildir después de todo: el formato codifica keywords
como una letra `a`-`z` en el nombre del archivo, y hay 26 letras.

> **Consecuencia de diseño (corrección al arbitraje A6).** El techo práctico de
> labels por carpeta es **26**, no "cientos". La L2 §2.3 ya lo previó — *"si una
> instalación lo alcanza, el engine rechaza crear el label con error claro (no
> hay labels solo-DB silenciosos)"* — y esta medición le pone el número.
>
> Lo importante es **cómo** hay que detectarlo: preguntarle al servidor no
> sirve. Dovecot acepta la keyword 27, la persiste en el índice y la devuelve
> en el siguiente FETCH. La verificación por read-back que usa
> `StoreFlags` **no detecta este caso**, porque en el momento de la lectura la
> keyword está realmente ahí. La pérdida ocurre después, en un rebuild de
> índice que puede pasar semanas más tarde — y entonces los labels 27+ de todos
> los mensajes desaparecen a la vez, en silencio.
>
> Por lo tanto el engine **debe imponer el límite de 26 por su cuenta**,
> contando labels asignados por carpeta antes de escribir, y rechazar la
> creación del label 27 con un error explícito. No es una validación defensiva
> opcional: es la única que puede atrapar esto.

### Integridad por debajo del techo

```
RESULT keyword-integrity: 60/60 keywords presentes tras la corrida
```

Nada se pierde retroactivamente por debajo del techo. La degradación es un
corte limpio en 26, no una corrupción progresiva.

---

## Decisiones que salen de V1

1. **A6 se confirma.** METADATA en `/private/vendor/moov/labels` es persistente,
   sobra en tamaño y es reconstruible. No se toma el fallback a DB.
2. **El límite de labels por carpeta es 26**, impuesto por Moov en el engine,
   con error explícito al intentar el 27. Nunca confiar en que el servidor
   rechace: no lo hace.
3. **`imap_metadata = yes` es un requisito de instalación documentado.** El
   default global de Mailcow es `no`; funciona por el bloque `protocol imap`.
4. El read-back de `StoreFlags` **no cubre** el desborde de keywords. Son dos
   problemas distintos y hace falta el contador propio.

## Pendiente para el diseño de labels (fuera del scope de E2)

- Dónde vive el contador de keywords por carpeta (probablemente `mailboxes` en
  el store, §2.3) y cómo se reconcilia si otro cliente crea keywords.
- Qué hace la UI cuando una carpeta llega a 26 labels.
- Si conviene reservar algunas de las 26 para keywords estándar que otros
  clientes ya usan (`$Forwarded`, `$MDNSent`, `NonJunk`), que consumen del
  mismo presupuesto.

## Reproducir

```bash
MOOV_IMAP_V1_PROBE=1 \
MOOV_IMAP_TEST_HOST=dovecot MOOV_IMAP_TEST_PORT=143 \
MOOV_IMAP_TEST_USER=moov-test@atmosfera.cloud \
MOOV_IMAP_TEST_PASSWORD=<secreto> MOOV_IMAP_TEST_INSECURE=1 \
  go test -count=1 -v -run TestV1 ./internal/imap/
```

Para V1.5 hay que además inspeccionar el disco, que es donde está el hallazgo:

```bash
doveadm force-resync -u moov-test@atmosfera.cloud <carpeta>
cat /var/vmail/<dominio>/<usuario>/Maildir/.<carpeta>/dovecot-keywords | wc -l
ls /var/vmail/<dominio>/<usuario>/Maildir/.<carpeta>/cur/
```

**Secretos:** la contraseña viaja solo por variable de entorno. No aparece en
ningún archivo de este repositorio, que es público.
