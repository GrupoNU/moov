# Assets de marca CorpPass para `mail.corppass.events`

> **De:** sesión CorpPass · **Para:** sesión Moov · **Fecha:** 2026-09-18
> Cierra el único pendiente propio de F3 (§9.2 punto 2 y §9.7 del L2): la marca del host
> estaba aplicada salvo los gráficos.

Los carga Moov con `moovctl branding set`. Nombres tal como los espera el comando:

| Archivo | Medidas | Qué es |
|---|---|---|
| `logo.png` | 505 × 215 | Logotipo completo, **fondo transparente** |
| `logo-dark.png` | 505 × 215 | Igual, con "Corp" y la bajada en blanco, para el tema oscuro |
| `icon.png` | 512 × 512 | **Cuadrado**: placa cyan redondeada con la "P" en blanco |

Todos PNG, como exige la spec (el SVG se rechaza a propósito: es XML que puede llevar
scripts, y esa pantalla es donde se tipean contraseñas).

El color de la placa es el de la marca, `#00B8A9`, el mismo `primary` que ya está en el
`branding.json` del host.

## De dónde salieron, y qué conviene saber

Se derivaron del logotipo que el portal ya sirve
(`apps/portal/src/assets/corppass-logo.png`), no de material nuevo. Tres cosas que
encontramos preparándolos, por si alguien los regenera:

1. **El original NO tenía transparencia.** Es RGBA pero con fondo blanco sólido: la
   cabecera del archivo dice "con canal alfa" y engaña. Puesto tal cual, en el tema oscuro
   se habría visto un **rectángulo blanco**, y el icono, una franja blanca sobre la placa
   cyan. El fondo se recortó por color, con el borde suavizado proporcional para no dejar
   dientes.

2. **El `favicon.png` del portal no servía de icono**: es la caja "Pass" recortada dentro
   de un lienzo cuadrado con márgenes blancos, no un símbolo. El `icon.png` se dibujó
   aparte.

3. **La "P" sola, y no el logotipo, tiene precedente**: es la misma decisión que se tomó
   para el icono de notificación de Android (el logo completo es ilegible a 24 px). Así el
   icono del webmail y el de la app cuentan lo mismo.

## Lo que NO resuelven estos archivos

La marca de Moov es **por host, no por casilla** — el contrato lo dice al crear una cuenta
("host brand inherited") y `L2-brand-admin.md` lo declara no-goal explícito
("per-mailbox branding"). Así que **todos los eventos ven la misma marca CorpPass dentro
del webmail**; no hay logo por evento ahí adentro.

Del lado de CorpPass el branding del evento sí manda, en lo que es nuestro: la tarjeta del
organizador y los correos que salen. Lo dejamos dicho para que no aparezca como sorpresa
en el gate.

## Si hace falta mejorarlos

Son derivados automáticos, suficientes para no bloquear F3. Si el equipo de marca quiere
versiones dibujadas a mano —sobre todo el `icon.png`, cuya "P" es geometría, no la
tipografía exacta del logotipo— se reemplazan sin tocar nada más: los flags que no se
pasan a `moovctl branding set` conservan su valor.
