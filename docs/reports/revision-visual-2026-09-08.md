# Revisión visual y funcional lado a lado — Moov vs Gmail (2026-09-08)

> **Método:** el dueño dejó abiertas en el navegador de automatización su Gmail personal (referencia) y su cuenta real de trabajo en Moov (`diego@gruponu.com`, mail.gruponu.com). Un conductor único recorrió ambas en **modo estrictamente lectura** y capturó 22 pares de superficies a pantalla completa (2560×1310, viewport verificado por pestaña); seis analistas independientes compararon cada sección con ojo de diseño y contra los canones 06/07, leyendo código solo para explicar defectos; el director auditó, cruzó las correcciones entre informes y **diagnosticó en el DOM vivo el P0 principal**. Las capturas contienen correo real: quedan **fuera del repo** (`.playwright-mcp/revision-evidence/`, git-ignorado); este documento es la evidencia commiteada.
> **Incidente declarado:** un clic del conductor en coordenadas "vacías" abrió un mensaje no-leído del dueño ("Alerta de seguridad", Google, 4 sept) y lo marcó leído. Ninguna otra escritura en ninguna de las dos cuentas.
> **Lo que NO se probó (requería escribir en la cuenta real):** archivar, destacar, posponer, silenciar, enviar, crear filtros/etiquetas, cambiar ajustes. Queda para una sesión con `moov-test`.

## 0. Veredicto en una línea

**La arquitectura Gmail está lograda y la capa de estado no.** Estructura (barra, riel, lista, lector, compositor flotante, panel rápido, página de ajustes) en su lugar; fallan los momentos en que la UI *responde*: hover, selección, menús, paginación, foco de teclado — y un P0 de layout que anula la vista dividida. **Cuatro de los siete P0 se arreglan con una línea.**

## 1. Los P0 (rompen el uso) — con causa localizada

| # | Hallazgo | Causa (verificada) | Fix |
|---|---|---|---|
| **P0-1** | **El panel de lectura "a la derecha" no divide: abrir un mail tapa la lista y el divisor no aparece** | **Diagnóstico del director en el DOM vivo** (descartadas las dos hipótesis del analista: el bundle servido es el correcto y la base tiene `readingPane:"right"`). Grilla computada `260 \| 0 \| 1771 \| 520 \| 0 \| 9` = 6 pistas en vez de 4. `MailScreen.module.css:208` `.dividerCell { grid-area: divider }` se aplica también en el layout "derecha", donde no existe el área nombrada → el divisor cae a una pista implícita al final y el lector ocupa la pista `auto` del divisor, dejando la lista (montada, 1 px) en `1fr = 0`. Invisible para jsdom. | **1 línea:** `.readingBottom .dividerCell { grid-area: divider }` (o `grid-column` explícita en `.reading`). Test de layout en navegador (Playwright) que asserte 4 pistas. |
| **P0-2** | **Menú de selección (Todos/Ninguno/…) translúcido: las filas se leen a través** | `MoveMenu.module.css:28` usa `--surface-overlay` = el **scrim** de `::backdrop` (`tokens.css:143`), no una superficie. Afecta 3 menús (+ `ActionBar.module.css:105`, `ReadingPane.module.css:521`). | **1 línea × 3:** `--surface-raised`. |
| **P0-3** | **La búsqueda ejecuta y navega mientras se tipea**; con `from:` a medias muestra warning + "Sin coincidencias" | `SearchBar.tsx:156-166` `handleChange → debouncer.run` (180 ms) → `MailScreen.tsx:2697` `replace()` de ruta. El debounce nació para `maxConcurrentRequests`, no para UX. | **1 línea:** quitar `debouncer.run` de `handleChange` (Enter, sugerencia y panel ya disparan). |
| **P0-4** | **Tras usar la búsqueda, `?` abre recientes en vez de la ayuda** (foco atrapado) | `SearchBar.tsx:226-239`: ninguna rama de Escape hace `blur()`, y `stopPropagation` corta `closeOverlay`. | **1 línea:** última rama de Escape → `blur()` + foco a la lista. |
| **P0-5** | **El riel es un volcado de ~25 carpetas IMAP** (Calendario, Diario, Fuentes RSS, Problemas de sincronización/Conflictos con badge 26…) | `mailboxes.ts buildMailboxTree` ordena rol→alfabético y `MailboxList` renderiza todo; no hay "Más" ni ocultamiento. Además **dos filas "Archivo"** (rol + custom). Colapsado = ~20 iconos idénticos. | Filas canónicas + "Más" plegado + carpetas de sistema ocultas por defecto; desambiguar "Archivo". Y su contraparte en ajustes: **tabla mostrar/ocultar carpetas** (Gmail la tiene; Moov la necesita más). |
| **P0-6** | **Lector: muro de 16 botones de texto en dos filas** donde Gmail pone 7 iconos | Decisión deliberada `ReadingPane.tsx:470-476` ("this pane has the room") — premisa que muere al arreglar P0-1 (lector de 520 px). | Fila de iconos con tooltip + resto en ⋮ (como ya hace `ListToolbar`). |
| **P0-7** | **Ajustes → Recibidos: Tema y Densidad son filas-puntero muertas** ("Abrir los ajustes rápidos") | `registry.ts:142-153` razonado (un solo control vivo por pref) pero mal concluido: Gmail nunca pone una fila que no hace nada. | Quitar del render; registro solo para que la búsqueda de ajustes navegue **al panel**. |

## 2. P1 por sección (rompen paridad de uso diario)

**Riel y barra (A):** orden Enviados/Borradores invertido y virtuales dibujadas distinto (A-03) · dos filas parecen activas a la vez — anillo de foco persistente (A-04) · la ruedita desaparece al colapsar el riel (A-06) · búsqueda pegada al wordmark, ~425 px vs ~560 centrada (A-07) · ninguna vista tiene título y Pospuestos vacío pierde la toolbar (A-11).
**Lista (B):** hover sin elevación (B-03) · 4º icono de hover cortado por tapar la fecha con degradado en vez de ocultarla (B-04) · sin "de N" en bandeja/búsqueda — honestidad firmada en `queryTotal`, falta la **cota** tipo Gmail "de más de 1.000" (B-01).
**Lector (C):** cuerpos en iframe de altura fija → scroll anidado y marco vacío de 320 px (C-03; restricción de sandbox legítima detrás — decisión de arquitectura, no parche) · "Descargar el mensaje original" en cada mensaje (C-04) · conversación abre con 2 de 3 expandidos porque `openId` es el representante del hilo (C-05) · sin doble chevron ni "N de M" (C-06).
**Compositor (D):** barra de formato arriba bajo el asunto en vez de al pie junto a Enviar (D-01) · "Descartar" como palabra junto al ⋯ en vez de papelera aislada a la derecha (D-02). *Corrección al conductor: el tamaño de la tarjeta NO es hallazgo — Moov es más angosta (≈525 vs ≈587 px).*
**Búsqueda (E):** sin sugerencias al tipear — el combobox está bien hecho y mal alimentado (E-04) · sin autocompletado de contactos (E-05) · sin total en resultados (E-13) · panel avanzado ~420 px colgado a la izquierda en vez de al ancho de la caja (E-19) · falta "No incluye" — omisión razonada (NOT rechazado) que hay que decirle al usuario (E-20).
**Ajustes (F):** miniaturas de Tema en negro sólido (F-11) · **sin tabla de carpetas del sistema** (F-28, contraparte de P0-5) · panel de lectura como `<select>` sin previews y ofreciendo un valor que el lector no honra (F-34) · cero previews en Recibidos (F-35) · filtros: promete "EN ORDEN" sin control de orden (F-41) · **sin importar/exportar filtros** — contradicción de posicionamiento con Sieve detrás (F-42) · encabezado versalita duplicado en cada pestaña (F-47). *Corrección al conductor: Firma y Respuesta automática SÍ existen (pestañas Cuenta y Reenvío, no capturadas).*

## 3. P2/P3 (pulido) — resumen

Contadores del riel como píldoras rellenas (A-09) · Redactar subdimensionado (A-10) · caret en vez de sliders (A-08/E-11) · ETIQUETAS vacío sin CTA (A-12/F-30) · barra de 11 verbos siempre visible y gris (B-06) · leídos sin fondo tintado (B-07) · paginación sin URL (B-12) · cursor j/k al ras del borde (B-13) · snippet con URLs de tracking (B-08) · escala de densidad corrida: Compacta 56 px > default Gmail ~40 (B-11) · avatar+punto añadidos, contador de hilo ausente (B-09) · sin chips de etiqueta en el asunto (C-07) · adjuntos sin miniatura (C-10) · "imagen incrustada no se puede mostrar" (C-11) · segmentado Texto plano/Formato duplicado del ⋯ (D-04) · Cc/Cco subrayados en acento (D-05) · Enviar rectángulo no píldora (D-06) · "Asunto Asunto" (D-07) · "De" duplica dirección sin caret (D-08) · Escape borra la consulta (E-28) · ayuda en una columna (E-31) · panel rápido 19rem vs ~22 (F-06) · "Normal" vs "Predeterminada" (F-08) · 10 filas de ajustes donde Gmail muestra 19 y sin ancho máximo (F-19/F-48) · etiqueta de fila no negrita (F-20).

## 4. Decisiones de criterio para el dueño (🔍)

1. **Programar envío** como reloj suelto vs `▾` adosado a Enviar (D-09): divergencia razonada en código (Enviar inequívoco; desaparece offline). Aceptar y documentar en canon §7, o adosar.
2. **Alcance de búsqueda por defecto "En esta carpeta"** (E-15): Gmail busca en todo el correo. ¿Cambiamos el default a toda la cuenta?
3. **Avatar de iniciales + punto azul en la fila** (B-09): adiciones sobre Gmail; el punto es redundante con la negrita. ¿Se quedan?
4. **Cuerpos en iframe con altura fija** (C-03): renderizar el HTML saneado en el DOM padre (sin sandbox de iframe) resuelve el scroll anidado pero cambia la postura de seguridad de 3 capas. Recomendación del director: **mantener el iframe** y dimensionarlo por `postMessage`-free heurística de altura (o auto-resize vía `ResizeObserver` del documento hijo con `allow-same-origin` en un origen dedicado) — a evaluar con Fable en su propia tanda.
5. **"Importantes" y categorías** siguen en fase IA (correcto, ya firmado).

## 5. Lo que está bien y se protege

Panel de ajustes rápidos — **el mejor par de la revisión** (acoplado, previews, sin scroll — gana a Gmail) · compositor flotante no-modal con Enviar azul abajo-izquierda y minimizado que conserva el borrador · página de ajustes ruteada con pestañas y columna de 340 px exacta · **prosa de ajustes mejor que la de Gmail** (explica el porqué) · búsqueda de ajustes (D-5) · **resaltado de términos en resultados** (Gmail no lo hace) · nota "un filtro no puede trasladar esto" · recorte de citas que **elimina** el texto del documento en vez de ocultarlo (más estricto que Gmail) · `PaneDivider` WAI-ARIA completo (solo faltaba montarlo bien) · combobox APG de manual · orden de columnas de la fila y estrella con relleno · honestidad del techo de 26, de Sieve server-side y de notificaciones foreground · modal de atajos mejor organizado que el de Gmail.

## 6. Correcciones a la evidencia (cadena de auditoría)

- Conductor §12 (compositor "el doble de ancho") → **falso**, medido (D).
- Conductor §06 (faltan Firma y Respuesta automática) → **falso**, existen en Cuenta/Reenvío (F-23).
- Conductor §20 (⋮ de vista "roto") → **no roto, sin cablear**: `ListToolbar` lo implementa con `disabled={false}`; `MailScreen:4328` no pasa `renderOverflow` (B-05).
- Analista C, hipótesis del P0-1 (bundle desfasado / prefs "none") → **ambas falsas**, verificadas por el director; causa real: `grid-area` huérfana (§1).

## 7. Propuesta de tandas (para decidir juntos)

| Tanda | Contenido | Talla |
|---|---|---|
| **T1 — Los siete P0** | Las 4 líneas (P0-1…P0-4) + toolbar de iconos del lector + filas muertas de Recibidos + curaduría del riel con "Más" y tabla de carpetas del sistema | M |
| **T2 — Estado de la lista** | hover con elevación y fecha oculta, leídos tintados, cota "de más de N", ⋮ de vista cableado, barra de verbos con selección, URL de página, margen j/k, snippet sin URLs, densidad | M |
| **T3 — Lector y conversación** | cuerpos sin scroll anidado (decisión §4.4), descargar-original al ⋮, expandidos por defecto, doble chevron + N de M, chips de etiqueta, píldoras Responder al pie | M |
| **T4 — Compositor y búsqueda** | formato al pie, papelera derecha, fila de 9 controles, sugerencias con valores de operador, panel al ancho de la caja, fecha ancla, total de resultados, ayuda 2 columnas | M |
| **T5 — Ajustes** | previews en Recibidos, panel de lectura como radios, import/export de filtros, orden de filtros, miniaturas de tema, densidad tipográfica y ancho máximo, encabezado duplicado | S-M |

Sin código hasta que el dueño lea esto y ordene las tandas.
