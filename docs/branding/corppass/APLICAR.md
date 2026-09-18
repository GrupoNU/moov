# Marca de `mail.corppass.events` — lista para aplicar

> **De:** sesión CorpPass · **Para:** sesión Moov · **Fecha:** 2026-09-18
> Diego revisó el webmail con la casilla funcionando y definió la marca. Acá está todo
> resuelto; es una sola corrida de `moovctl branding set`.

## Estado hoy

```
GET https://mail.corppass.events/branding
{"name":"CorpPass Mail", …, "logoUrl":"", "logoDarkUrl":"", "iconUrl":"", …}
```

**Los tres gráficos están vacíos**: los PNG que dejamos en esta misma carpeta el 17/09
nunca se cargaron. Por eso el organizador ve la "M" genérica de Moov en la barra y la
pestaña del navegador sin favicon.

## El comando

```bash
moovctl branding set -host mail.corppass.events \
  -name "CorpPass Mail" \
  -short-name "CorpPass" \
  -tagline "El correo de tu evento" \
  -logo      docs/branding/corppass/logo.png \
  -logo-dark docs/branding/corppass/logo-dark.png \
  -icon      docs/branding/corppass/icon.png \
  -color-primary '#008076' \
  -color-on-primary '#ffffff'
```

(Los tres PNG están en esta carpeta. `supportUrl`, `privacyUrl` y `termsUrl` los definimos
aparte: hoy no hay una decisión tomada y preferimos no inventar URLs.)

## Por qué `#008076` y no el cyan de la marca

El color de CorpPass es `#00B8A9` — el de la caja "Pass" del logotipo. **No lo mandamos
tal cual a propósito**, por lo que dice su propio README ("Choosing the colour"): el
primary tiene que leerse como texto sobre blanco, y `#00B8A9` da **2,49:1**. Moov lo
ajustaría igual, y ustedes advierten que un color muy ajustado "no va a parecerse a la
marca del cliente".

Así que hicimos el ajuste nosotros, conservando el tono: `#008076` es el mismo cyan
oscurecido un 30 %, y es **el primer paso que cumple** (4,83:1). Debería volver **exacto,
sin ajuste**, y sin la línea de aviso en la consola. Si igual lo ajustan, avísennos: sería
una diferencia entre su criterio y nuestra medición, y preferimos saberlo.

El logotipo conserva el `#00B8A9` original — ahí el color vive en el gráfico, no en un
texto, así que no hay nada que contrastar.

## Lo que Diego pidió, en una línea

**"Los colores de Gmail"**: barra blanca, buscador gris, texto oscuro, y el color reservado
para un solo acento. Por lo que leemos en su README (`primary` = "the accent: buttons,
links, focus rings") **la PWA ya se comporta así**, y lo que se veía pintado de cyan a
pantalla completa era el estado sin marca aplicada. Si con estos valores la barra sigue
saliendo de color, díganlo y lo miramos juntos.

## Después de aplicar

El README avisa que los íconos de una PWA **instalada** se cachean fuerte: para ver el
ícono nuevo hay que **desinstalar y reinstalar** la PWA, no alcanza con recargar. Diego la
tiene instalada, así que conviene que lo sepa antes de que parezca que no funcionó.

## Referencia de valores

| Campo | Valor | Por qué |
|---|---|---|
| `name` | `CorpPass Mail` | ya está bien, no cambia |
| `shortName` | `CorpPass` | 8 caracteres, entra en el límite de 12 |
| `tagline` | `El correo de tu evento` | ya está bien, no cambia |
| `primary` | `#008076` | cyan de marca, ajustado por nosotros para pasar AA |
| `onPrimary` | `#ffffff` | 4,83:1 sobre el primary |
| `logo` | `logo.png` 505×215 | fondo transparente |
| `logoDark` | `logo-dark.png` 505×215 | tinta en blanco, caja cyan intacta |
| `icon` | `icon.png` 512×512 | cuadrado, placa cyan con la "P" |
