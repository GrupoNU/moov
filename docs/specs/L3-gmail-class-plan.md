# Plan L3 — De "funciona" a Gmail-class

> **Estado:** v2 — BORRADOR del director **reescrito tras auditoría adversarial** (agente independiente, 2026-08-26); pendiente de revisión del director y firma de Diego
> **Fecha:** 2026-08-26 · **Autor v1:** Fable 5 (director técnico) · **Auditor v2:** agente independiente (Fable 5), con verificación directa contra el código
> **Origen:** el veredicto del dueño del producto sobre la PWA en producción — *"no tiene nada de configuración, cuando digo nada es nada absolutamente… no hace ni el 20% de lo que hace Bulwark"* — y la regla rectora 1 del proyecto: vara Gmail-class **en todas las etapas**, con Bulwark como **piso a superar**, no como techo.
> **Evidencia:** `docs/research/05-gmail-class-surface.md` (relevamiento: código completo de Bulwark v1.9.2, taxonomía documentada de Gmail, estado real de Moov, tabla de brechas de ~300 filas). El registro de qué afirmaciones se verificaron contra el código y cuáles no, en §9.

---

## 1. El diagnóstico honesto

**El dueño tiene razón, literalmente.** `web/src/screens/settings/SettingsDialog.tsx` tiene una sección, una fila, tres radios de tema; el comentario *"THE NEXT SETTING GOES HERE"* (líneas 131-135) es todo lo demás. Bulwark tiene **26 pestañas en 6 grupos** y ~95 preferencias. **Verificado contra el código en esta auditoría.**

**Y el error de dirección fue de método, no de ejecución.** Cada épica de la PWA se verificó contra sus propios criterios técnicos (tests verdes, contraste AA, latencias medidas) y **ninguna se comparó jamás con la referencia**. Se construyó un cliente que funciona en vez del producto que se pidió. La spec de la PWA incluso advertía que el pulido sería el 80% de la percepción; aun así se dieron por buenas épicas que nadie puso al lado de Bulwark.

**La brecha tiene mejor forma de lo que el veredicto sugiere, pero peor de lo que la v1 de este plan decía.** El relevamiento la parte en tres:

| Categoría | Ítems | Carácter |
|---|---|---|
| **(a)** el servidor ya lo hace, la UI no lo expone | **15** | 7 son XS. Cero trabajo de protocolo, cero migraciones, cero dependencias |
| **(b)** un RFC lo define, el servidor no lo implementa | **16** | Sieve y Contactos son los anclas XL. ⚠️ Vacaciones NO es independiente: en Dovecot depende de la misma fundación ManageSieve que Sieve (ver GA-3) |
| **(c)** no existe en ninguna capa | **20** | Un ítem fundacional (persistencia de ajustes) + vista de conversación + offline |

**Corrección a la v1 de este plan:** la v1 decía *"la persistencia de ajustes desbloquea casi todo el bloque (a)"*. **Es falso.** Los ítems más valiosos de (a) — firma, nombre para mostrar, Reply-To, Bcc — persisten hoy vía `Identity/set` (tabla `identities`, ya en producción); la vista de no-leídos, la búsqueda por fechas, el orden por destacados, el CRUD de carpetas y la paginación no necesitan persistencia alguna para funcionar. La persistencia desbloquea únicamente los **defaults de preferencia** (densidad, ventana de undo, política de imágenes, idioma, orden por defecto). Sigue siendo fundacional y va primero — pero no es la ruta crítica de la queja: es *una* de dos rutas paralelas. La otra es exponer lo que ya existe.

**Y hay ausencias que la v1 ni nombró** (detalle en §4 y §5): la PWA **no tiene botón de spam** (verificado: cero ocurrencias de spam/junk en `ActionBar.tsx` y `shortcuts.ts` — y marcar spam es un MOVE a Junk que entrena Rspamd gratis, ADR §4); el lector no tiene imprimir/ver-original/marcar-no-leído/mover/siguiente-anterior; no hay undo de acciones (`z`) pese a que las inverse patches ya existen; no hay autocompletado de direcciones al redactar; el outbox con fallos de envío es invisible; y el techo `MaxQueryReach=10000` choca contra el criterio ADR §6 de 100k **y contra la cuenta real del dueño (26.869 mensajes)**.

## 2. Principios que gobiernan este plan

**G1 — El piso es Bulwark, medido, no recordado.** Cada fase se cierra con una comparación explícita contra Bulwark sobre los mismos flujos. Ninguna épica se da por buena solo por tener sus tests verdes: ese fue el error que produjo esta situación.

**G2 — Copiamos la arquitectura de información, no el mecanismo.** Bulwark guarda ajustes en localStorage + un archivo cifrado en su propio backend Next.js: es un parche por no tener servidor. Nosotros tenemos PostgreSQL y JMAP. La IA de 26 pestañas es producto de años de decisiones reales y se respeta; la implementación es nuestra.

**G3 — Los 20+ mecanismos que el relevamiento extrajo del código de Bulwark son requisitos, no inspiración.** Cada uno codifica un bug de producción que ellos ya pagaron. Se implementan con su test de regresión y su cita al issue de origen. **Y con una regla de orden que la v1 no tenía: cada mecanismo aterriza EN LA MISMA ÉPICA que la feature que dispara su bug, nunca después** — `retainedInViewIds` CON la vista de no-leídos (o las filas desaparecen bajo el cursor desde el día 1), el escape de JSON Pointer ANTES de labels anidadas, `batched()` de request-limits ANTES de las acciones de hilo completo (un hilo real de 24 mensajes ya excede techos de llamadas por request).

**G4 — Lo que ya hacemos mejor se defiende.** UI optimista con rollback real, threading server-side sobre todo el store, FTS instantáneo, sanitizador de tres capas + proxy de imágenes HMAC (Bulwark no proxea), tokens con alcance acotado, `Email/changes` (que Bulwark ni siquiera llama). El plan no puede degradar nada de eso al agregar superficie.

**G5 — Sin huecos silenciosos.** Si algo no está implementado, la UI lo dice; jamás un control que no hace nada ni un estado vacío ambiguo. **Y el plan se aplica a sí mismo la regla:** todo lo que se difiere está nombrado en §5 con su tamaño, para decisión del dueño — no desaparece del documento. La v1 omitía snooze, schedule send, mute, plantillas y cambio de contraseña sin decir que los omitía; eso era exactamente el hueco silencioso que G5 prohíbe.

**G6 — Todo ítem lleva talla y toda fase lleva total.** El dueño firma sabiendo qué compra. Escala del relevamiento: XS ≤ ½ día · S 1–2 d · M 3–5 d · L 1–2 sem · XL > 2 sem. Son juicio de ingeniería, no compromisos contractuales — pero se re-estiman al abrir cada fase, no al cerrarla.

## 3. Arbitrajes

**GA-1 — La persistencia de ajustes es servidor, vía JMAP, y va en la primera fase.** Lo que es semánticamente identidad ya vive en `Identity` (firma, nombre, Reply-To, Bcc — **el servidor ya lo implementa completo**, `internal/jmap/mail/identity.go`; no hay que extender nada, solo construir el formulario). Para lo demás se crea un objeto de preferencias de cuenta (densidad, atajos, idioma, ventana de undo, imágenes remotas, orden por defecto) servido por métodos JMAP `get`/`set` estándar sobre tabla propia, con cursor de estado. **Requisitos que la v1 no fijaba:** (1) capability con namespace vendor propio (p. ej. `https://moov.email/ns/prefs`) — JMAP no tiene objeto de preferencias estándar y no vamos a fingir que sí; (2) **schema versionado desde el día 1** con cadena de migración — Bulwark va por la versión 7 de su cadena y cada salto codifica un incidente; (3) verificación en CI de que Bulwark ignora limpiamente la capability desconocida (sigue siendo nuestro oráculo de regresión). **Razón de fondo:** un ajuste que vive en el navegador se pierde al cambiar de dispositivo — y el producto se vende como webmail multi-dispositivo. Bulwark hace lo contrario porque no puede hacer otra cosa.

**GA-2 — Vista de conversación es la prioridad de experiencia, y la secuencia lo respeta de verdad.** La v1 declaraba esto ("por encima de cualquier ajuste") y luego la ponía íntegra detrás de la fase de ajustes — una contradicción interna. Resolución: la Fase A es corta (~2 semanas) y sus scopes (panel de ajustes, formularios de identidad) son **disjuntos** del lector; **B1 (conversación) arranca en paralelo con A** apenas A1 esté en curso. El corazón de la experiencia no espera a que exista un selector de densidad.

**GA-3 — Sieve entra al plan completo, con una corrección de dependencias que cambia la Fase C.** La v1 presentaba Vacaciones como *"barato y alto impacto"*, independiente de Sieve. **Es incorrecto en nuestro stack:** en Dovecot la respuesta automática ES un script Sieve (extensión `vacation`); no existe otro mecanismo, y RFC 9661 §4 lo formaliza (el script `vacation` es server-managed, modificable solo vía `VacationResponse/set` — Bulwark mismo lo salta en su filter-store por esa razón). Por lo tanto la Fase C abre con una **fundación ManageSieve compartida (C0)** — cliente contra `dovecot:4190` con la app password que ya tiene scope `sieve` (ADR §4), encapsulado con la misma disciplina que `internal/imap` — y Vacaciones y Filtros se montan sobre ella. El particionado por origen de Bulwark (nunca destruir un script escrito a mano o por otro cliente) se adopta entero: es nuestra invariante "Dovecot es la fuente de verdad" aplicada a Sieve.

**GA-4 — Offline se mantiene en la última fase; instalabilidad NO.** Bulwark no tiene offline en absoluto (su service worker tiene el handler de fetch vacío, por diseño) — no es donde perdemos la comparación, y el offline real (SW + IndexedDB + outbox) es L. Pero **"PWA instalable" es un criterio medible del ADR §6, cuesta S** (manifest + iconos + SW mínimo) **y hoy el producto que se llama PWA no es instalable** (verificado: `web/public/` contiene solo `favicon.svg`; no hay manifest ni SW). Instalabilidad se adelanta a la Fase B; offline queda en D. Son dos ítems distintos que la v1 fusionaba.

**GA-5 — Lo diferido se difiere con nombre, tamaño y firma del dueño.** Ver §5. Nada sale del plan por omisión.

## 4. Las fases

Cada fase termina con **comparación explícita contra Bulwark** (G1) y con el dueño pudiendo usarla. Tamaños por G6; el total de cada fase incluye integración y la comparación de cierre.

### Fase A — Fundación de ajustes + exponer lo que ya existe *(total ≈ 2 semanas)*

*La mitad de la queja se responde acá; la otra mitad (conversación) arranca en paralelo — GA-2.*

- **A1 — Persistencia de preferencias (servidor).** [M] Migración, objeto de preferencias con schema versionado, métodos JMAP `get`/`set` bajo capability vendor, cursor de estado, cableado en sesión, test de que Bulwark ignora la capability. Modelo: GA-1. Semántica de merge per-account desde el día 1 (el bug #507 de Bulwark: el mapa global que el último login pisa).
- **A2 — El panel de ajustes real.** [M] La IA de Bulwark adaptada: Cuenta (identidad, firma, alias), Correo (lectura, redacción, envío, undo), Apariencia (tema, densidad, idioma), Carpetas, Filtros (esqueleto honesto: "llega en Fase C", G5), Avanzado. Navegable por teclado. **Paridad de claves i18n forzada en CI desde esta épica** (hoy: 2 locales × 228 claves sin verificación).
- **A3 — Lo que NO depende de A1** *(≈ 3-4 días en total)*: firma texto+HTML [XS — `Identity/set` ya lo implementa y la PWA ya la lee para el compose], nombre para mostrar / Reply-To / Bcc por defecto [XS], Reply-To en salientes [XS], vista de no-leídos [XS] **con `retainedInViewIds` en la misma épica** (G3), búsqueda por rango de fechas [XS], orden por destacados/no-leídos [XS — el sort `hasKeyword` del servidor consulta bitmask y array, verificado en `adapter_query.go`], **marcar spam / no-spam** [S — hoy inexistente en la PWA; servidor listo (MOVE a Junk), atajo `!`, entrena Rspamd vía imapsieve], **completar el lector**: imprimir, ver original/cabeceras (el blob crudo ya se descarga), marcar no-leído, mover, siguiente/anterior [S].
- **A4 — Lo que SÍ depende de A1**: ventana de undo configurable [S — ⚠️ **no es solo UI**: hoy `UndoWindow` es config global del daemon (`cmd/moovd/jmap.go:137`); hay que plumbear el valor per-account desde preferencias hasta el enqueue del outbox, dentro del clamp [5 s, 30 s] ya pinneado por test], densidad efectiva [S], política de imágenes remotas por defecto [XS], idioma manual [XS].
- **A5 — Mecanismos de G3 que aplican YA** *(≈ 3 días)*: atajos por `event.code` [XS — verificado: hoy resolvemos por `event.key` y perdemos todo teclado no-latino], escape RFC 6901 de JSON Pointer [XS — obligatorio antes de C4], `RequestTimeoutError` jamás reintentado [test sobre nuestro cliente — reenviar un `EmailSubmission/set` es enviar el mail dos veces], separar create de destroy en borradores [test], `Message-ID` generado por el cliente + omitir `Cc` vacío [verificación del ensamblado W3, con test], los dos bypasses de DOMPurify documentados + normalización C0/CSS-escape en URLs [S], **rebuild de `srcDoc` al desbloquear imágenes pinneado por test** [XS — hoy ya se recompone por `useMemo` en `SecureHtmlBody.tsx`; el test evita que una refactorización lo rompa en silencio], **`coalesceRefresh` sobre el refresh por SSE** [S — nuestro fan-out tiene la misma forma que el que a Bulwark le blanqueó el árbol de carpetas], **error boundaries por panel** [S — hallazgo tardío del relevamiento: Bulwark compartimenta la UI en 4 dominios de fallo independientes (sidebar, lista, lector, compositor), cada fallback dimensionado para no colapsar el layout y el del compositor con recuperación real en vez de reset en el sitio. ~230 líneas para que un crash del lector no se lleve puesta la aplicación entera; es la parte mejor descompuesta de su código].

### Fase B — Conversación, lista y búsqueda *(total ≈ 3-4 semanas; B1 arranca en paralelo con A)*

*El corazón de la experiencia (GA-2) y los criterios medibles del ADR §6 que la v1 dejaba caer en silencio.*

- **B1 — Conversación.** [L] Hilo expandible en línea, mensajes colapsados con resumen, acciones por mensaje, cita plegada, acciones de hilo completo (archivar/borrar hilo) **con `batched()` de request-limits aterrizando antes** (G3 — el hilo real más grande de la cuenta 2 tiene 24 mensajes). Incluye `QuotedHtml` como nodo atómico (la cita original nunca se re-parsea; Outlook/MJML sobreviven), atajos `n`/`p` dentro del hilo, y la decisión explícita sobre `Thread/changes` (hoy no registrado: registrar o documentar el skip — nunca un `unknownMethod` sorpresa).
- **B2 — Carpetas con CRUD completo.** [S UI + M server] Crear, renombrar, mover/anidar, borrar, suscribir — el servidor ya lo soporta. Respetar `myRights` en toda la UI (hoy solo el menú de mover lo consulta — `ActionBar.tsx:256`). **Deuda medida:** borrar carpeta tarda 1,8-6,1 s (W4b) y viola la vara <100 ms; se ataca acá (server) y si el fix IMAP no alcanza, la UX lo hace honesto (borrado optimista con estado en el nodo), jamás un botón que congela.
- **B3 — Paginación real + refresco incremental.** [M] Scroll infinito con cursor (`position`/`limit` ya soportados), `Email/changes` para el refresco (donde superamos a Bulwark, que refetchea todo por cada push), **cutoff de merge derivado del tamaño de página** (G3 — el bug de los borradores fantasma que re-enviaban mail). **Y la decisión que la v1 esquivaba:** `MaxQueryReach=10000` (`search.go:42`) contra el criterio ADR §6 de "scroll fluido con 100k+" **y contra la cuenta real de 26.869 mensajes** — el scroll infinito de esta épica choca el techo en el buzón del propio dueño. Opciones a arbitrar con números: subir el cap re-validando los shapes de S3, o paginación profunda por anchor/fecha (keyset real, sin OFFSET). No se cierra B3 sin resolverlo o sin que Diego firme el techo como límite explícito de producto.
- **B4 — Búsqueda con operadores + resaltado.** [M UI + M server] Panel de filtros, chips, `from:`/`to:`/`subject:`/`before:`/`after:`/`has:`/`in:`, sugerencias recientes. **Trabajo de servidor que la v1 subdeclaraba:** separar from/to/subject del tsvector compartido (hoy sobre-coinciden los cuatro, documentado), `hasAttachment`/`cc`/`bcc` [S — el propio código nombra estas primero: las columnas ya existen], **`is:starred`** [S — verificado: el filtro `hasKeyword $flagged` hoy se RECHAZA (los system flags viven en bitmask, `query.go:882`); falta el predicado], `OR`/`NOT` [M — decidir alcance]. Y **`SearchSnippet/get` (RFC 8621 §5) para resaltar resultados** [M] — Bulwark no lo llama nunca: acá les ganamos a ambos referentes.
- **B5 — Undo de acciones (`z`).** [S] Toast con deshacer para archivar/borrar/mover. **Las inverse patches ya existen** (nuestro rollback optimista); esto es surfacearlas con un timer. Gmail lo tiene como CORE; Bulwark no lo tiene en absoluto — diferenciación barata que la v1 omitía.
- **B6 — Outbox visible.** [S] Estado de envíos, fallos y cancelaciones desde `EmailSubmission/get`+`/changes` (servidor listo; `/query` sigue sin registrarse — documentado, Bulwark tampoco lo llama). Un envío que falla en silencio es inaceptable en un cliente diario.
- **B7 — PWA instalable.** [S] Manifest + iconos + SW mínimo (GA-4). Offline real queda en D.

### Fase C — Filtros, vacaciones, cuotas y redacción diaria *(total ≈ 4-5 semanas; el XL de Sieve domina)*

- **C0 — Fundación ManageSieve.** [M] Cliente contra `dovecot:4190` con la app password (scope `sieve` ya aprovisionado), encapsulado tras interfaz propia con la disciplina de `internal/imap`. **Prerrequisito de C1 y C2** (GA-3).
- **C1 — Respuesta automática.** [M, sobre C0] RFC 8621 §8 `VacationResponse/get`/`set` (singleton), materializada como script Sieve server-managed. Anunciar la capability sola ya enciende la pestaña de Bulwark.
- **C2 — Filtros/reglas.** [XL] RFC 9661 `SieveScript` (get/set/validate + upload de blob) con particionado por origen y round-trip que preserva scripts ajenos (el tokenizer de fallback de Bulwark marca el orden de magnitud: ~800 líneas). Constructor visual en la UI. **Incluye como recetas del builder: reenvío automático y remitentes bloqueados** — Gmail los presenta como features de primer nivel; son reglas Sieve, y nombrarlos evita que desaparezcan dentro de "filtros".
- **C3 — Cuotas.** [S] RFC 9425 `Quota/get` + barra de uso en Cuenta (Mailcow ya expone quota; nunca la leímos).
- **C4 — Etiquetas/keywords en la UI.** [M] Con el techo real de **26 keywords durables por mailbox** (medido en V1) enforced y explicado en la UI, no escondido; paleta acorde al techo (la de 39 colores de Bulwark asume una libertad que Maildir no da); rename como `migrateKeyword` acotado. Requiere el escape de JSON Pointer de A5 ya aterrizado (G3).
- **C5 — Notificaciones de escritorio (foreground).** [S] Notification API sobre el SSE que ya tenemos — visible e inmediato. Web Push de fondo (VAPID propio) queda en D3; eran dos ítems distintos fusionados en la v1.
- **C6 — Autocompletado de direcciones.** [M] Desde el propio correo del usuario (índice de direcciones vistas en `messages`), **sin** subsistema de contactos. Un cliente diario donde cada destinatario se tipea a mano completo no pasa ninguna comparación con Gmail, Fastmail ni Bulwark; ni el relevamiento ni la v1 lo nombraban por debajo del XL de "Contactos". El subsistema de contactos completo sigue diferido (§5).

### Fase D — Pulido Gmail-class y cierre *(total ≈ 3-4 semanas)*

- **D1 — Densidad, idioma y accesibilidad completa.** [M-L] i18n con paridad forzada (exigida desde A2 — acá se completa la superficie); decisión explícita sobre RTL (hoy: nada); reduced-motion y auditoría focus-visible (los dos gaps a11y conocidos); **profundidad de teclado Gmail**: `z` (B5), `[`/`]` archivar-y-avanzar, `v` mover, `l` etiquetar, selección `* a`/`* n` — sobre nuestro resolver puro, que ya es mejor arquitectura que el switch de Bulwark.
- **D2 — Offline real.** [L] SW + IndexedDB + outbox offline + `recycleStaleSSE` en `visibilitychange` (G3 — iOS congela los timers de una PWA en pantalla de inicio). Territorio de diferenciación pura: Bulwark no tiene nada y Google recomienda un marcador.
- **D3 — Web Push.** [L] Con nuestras claves VAPID, sin el relay de terceros de Bulwark.
- **D4 — Auditoría comparativa final** contra Bulwark y Gmail **sobre los criterios medibles del ADR §6, uno por uno y con números**: búsqueda y toda acción <100 ms percibidos (incluida la deuda de borrar carpeta), scroll con el buzón real completo (B3 resuelto), push real, teclado completo, undo send + undo de acciones, PWA instalable + offline. Ningún criterio del §6 puede quedar sin fila en el informe de cierre — la v1 no exigía esto y así fue como se perdió el §6 la primera vez.

**Total estimado del plan: ~12-15 semanas de trabajo de ejecución** (con paralelismo A∥B1 y los modelos por complejidad de la jerarquía del proyecto). Es más de lo que la v1 sugería sin decirlo — no traía tamaños. El dueño firma esto sabiendo la magnitud.

## 5. Diferido con nombre — decisión del dueño, no deriva del plan (GA-5)

| Ítem | Tamaño | Por qué se difiere | Riesgo de diferirlo |
|---|---|---|---|
| Snooze | L | Sin estándar JMAP; requiere timer store-side + estado propio | Gmail/Superhuman lo tienen como CORE; se notará en la comparación D4 |
| Schedule send | M | La tabla `intents` ya existe; es un `sendAt` futuro + scheduler | El más barato de esta lista; candidato natural si sobra capacidad en C |
| Mute de conversación | M | Necesita flag a nivel hilo + las 3 excepciones de Gmail | Bajo — Bulwark tampoco lo tiene |
| Plantillas | S | Puro cliente + preferencias (A1 lo habilita) | Bajo; candidato a colarse en C |
| Seguridad de cuenta (cambio de contraseña, TOTP) | M-L | Vía API de Mailcow (la app password ya se aprovisiona por ahí); no hay análogo Dovecot | Un panel de "Cuenta" sin cambio de contraseña es un hueco visible; **decisión explícita de Diego requerida** |
| Contactos (subsistema completo) | XL | Producto aparte por orden ya fijado (webmail → IA → módulos); C6 cubre la necesidad diaria | Bajo con C6 hecho |
| Multi-cuenta, calendario, archivos, plugins, S/MIME, import `.eml` | XL c/u | Productos o módulos aparte; orden fijado por el dueño | Conocido y aceptado |

Si alguno resulta indispensable para la comparación, entra por decisión del dueño, no por deriva del plan.

## 5 bis. Decidido y descartado: avatares de remitente con logo (2026-08-27)

El dueño observó que Bulwark muestra logos de marca donde Moov muestra iniciales, y pidió analizarlo antes de decidir. **Resultado: se descarta; las iniciales se mantienen.**

Lo que la investigación estableció, verificado contra el código de Bulwark y la documentación de los referentes:

- **BIMI queda refutado como explicación.** Bulwark no lo implementa (cero ocurrencias en su árbol). Lo que hace es pedir el **favicon del dominio del remitente a DuckDuckGo** desde su propio servidor.
- **Gmail y Fastmail no rascan favicons.** Ambos muestran logo solo vía BIMI, con DMARC en enforcement; Gmail además exige certificado VMC/CMC. Los logos "de más" que muestra Bulwark son **exactamente los que Gmail decidió no mostrar**.
- **Un favicon lo controla el dueño del dominio del From.** Un lookalike (`paypa1.com`) pasa su propia autenticación y sirve el logo copiado: la imagen presta credibilidad justo donde no la hay. El gating por autenticación no lo evita.
- **BIMI depende de un trámite del remitente** (DMARC estricto + SVG Tiny PS + registro DNS + certificado pagado para Gmail) que la enorme mayoría de los corresponsales reales del piloto nunca va a hacer, y **Outlook —dominante en el mercado corporativo local— no lo soporta**. La inversión compra cobertura baja.

**Decisión del dueño:** no se invierte en logos de remitente; se prioriza seguridad y confiabilidad, que es además la posición de Gmail. Las iniciales coloreadas son el comportamiento definitivo, no un placeholder.

**Consecuencia para G1 (comparación por fase):** Bulwark mostrará más logos que Moov y eso **no cuenta como brecha** — es una diferencia deliberada de política, documentada acá para que ninguna comparación futura la reabra como defecto.

**Lo que sí queda vivo, en otro carril:** configurar DMARC estricto (y opcionalmente BIMI) en los dominios propios del grupo es trabajo de infraestructura de VPS_Mail, no de Moov. Vale por deliverabilidad y por protección contra suplantación; el logo es un extra que solo algunos clientes honran.

## 6. Riesgos

1. **La superficie crece más rápido que la calidad.** Mitigación: G1 — cada fase se compara contra Bulwark antes de cerrarse, y el dueño la usa.
2. **El tope de 26 keywords de Maildir** limita el modelo de etiquetas frente a Gmail. Medido (V1); se explica en la UI, no se esconde (C4).
3. **Sieve es XL y toca Dovecot** — y ahora también sostiene Vacaciones (GA-3). Riesgo de romper scripts existentes; mitigado por el particionado por origen y por C0 como fundación única y testeada.
4. **Borrar carpeta tarda 1,8-6,1 s** (W4b) y viola la vara. Entra como deuda a resolver en B2, con salida UX honesta si el fix IMAP no alcanza.
5. **La deuda de i18n crece con cada pantalla nueva.** Paridad de claves exigida en CI desde A2.
6. **El techo de alcance de búsqueda/scroll (`MaxQueryReach=10000`) contra el criterio ADR §6 (100k) y contra la cuenta real (26.869).** La v1 no lo mencionaba: el scroll infinito de B3 lo golpea en el buzón del propio dueño. Se resuelve en B3 con números o se firma como límite explícito — nunca queda implícito.
7. **Drift del esquema de preferencias.** Cada preferencia nueva de las fases B-D muta el objeto de A1; sin versionado y cadena de migración desde el día 1, compramos la clase de bugs que a Bulwark le costó 7 versiones (GA-1 lo exige).
8. **El oráculo Bulwark.** (a) Toda capability vendor nueva debe verificarse como ignorada limpiamente por Bulwark en CI — si el oráculo deja de funcionar contra nuestro server, perdemos la red de regresión; (b) nota operativa: el Bulwark desplegado en el piloto es v1.8.1 **con un advisory publicado sin parchear** (GHSA-24w9-8r42-8jwm, SSRF); actualizarlo a 1.9.2 es tarea de operación del piloto, fuera de este plan pero no de la vista del dueño.

## 7. Aprobación

- [x] Auditoría del plan por agente independiente (2026-08-26 — esta v2)
- [x] Revisión de esa auditoría por el director (2026-08-26). **Aprobada sin objeciones.** La auditoría desmontó cinco afirmaciones de la v1 con el código en la mano — la más costosa, que Vacaciones fuera independiente de Sieve: en Dovecot la respuesta automática ES un script Sieve, así que la v1 escondía una dependencia XL detrás de un ítem "barato". También corrigió la contradicción interna de la v1 (declarar conversación como prioridad y secuenciarla última), nombró ausencias que ni el relevamiento ni la v1 vieron (spam, undo de acciones, autocompletado de direcciones, outbox visible, PWA instalable como criterio propio), y rescató el criterio de scroll con 100k del ADR §6 que la v1 dejaba caer en silencio. El director acepta el plan v2 como base de ejecución.
- [ ] Firma de Diego y ejecución

---

## 8. Registro de auditoría (v1 → v2)

### 8.1 Qué se verificó de la v1 contra el código — y resultó correcto

- **17 métodos registrados**, exactamente los del relevamiento (`internal/jmap/mail/register.go`, `identity.go:126-128`, `submission.go:201-203`); `EmailSubmission/query` y `Thread/changes` efectivamente no registrados.
- **`SettingsDialog.tsx`: una sección, una fila, tres radios**, y el comentario "THE NEXT SETTING GOES HERE" (líneas 131-135).
- **Atajos por `event.key`** (`web/src/keyboard/shortcuts.ts:101,116,123`) — la regresión de teclados no-latinos es real.
- **La PWA jamás envía `sort`** en `Email/query` (`web/src/mail/api.ts:177-183`) y **ya lee `Identity`** para la firma (`write.ts:708`, `Composer.tsx`).
- **`Identity/set` implementa** name/replyTo/bcc/textSignature/htmlSignature con create→`forbiddenFrom` (`identity.go`) — la afirmación "el servidor ya lo hace, solo falta el formulario" es cierta para todo el bloque identidad.
- **Clamp de undo [5 s, 30 s]** (`submission.go:154-155`) y **`MaxQueryReach=10000`** (`search.go:42`).
- **El sort `hasKeyword` funciona con `$flagged`** (consulta bitmask y array — `adapter_query.go:219`), así que "orden por destacados" sí es categoría (a).
- **El rebuild de `srcDoc` al desbloquear imágenes ya ocurre por construcción** (`SecureHtmlBody.tsx`, `useMemo` sobre `allowRemoteImages`) — el mecanismo de G3 pasa de "implementar" a "pinnear con test".
- Los números del diagnóstico (26 pestañas, ~95 preferencias, 15/16/20 ítems, 2×228 claves i18n) coinciden con el relevamiento.

### 8.2 Qué se corrigió — y por qué

1. **"La persistencia desbloquea casi todo el bloque (a)"** — falso: los ítems más valiosos de (a) persisten vía `Identity/set` o no necesitan persistencia. Reescrito en §1 y reflejado en la partición A3/A4. Un plan que repite el error de inventario de la v0 repetiría el fallo original.
2. **Vacaciones como "barato" e independiente de Sieve** — falso en Dovecot: la respuesta automática es un script Sieve; requiere la fundación ManageSieve. Fase C reestructurada con C0 compartido (GA-3). Este era el error de secuenciamiento más caro del plan: habría descubierto la dependencia con C1 a medio construir.
3. **"Ventana de undo configurable" como ítem (a)/XS de pura UI** — hoy `UndoWindow` es config global del daemon, no per-account; requiere plumbing servidor (A4, S).
4. **"`myRights`, que hoy servimos e ignoramos"** — parcialmente falso: `ActionBar.tsx:256` ya lo consulta para destinos de mover. Ajustado en B2.
5. **Contradicción GA-2 vs orden de fases** (conversación "por encima de cualquier ajuste" pero secuenciada íntegra detrás de ellos) — resuelta con A∥B1 sobre scopes disjuntos.
6. **Instalabilidad fusionada con offline** en D2 — separadas (GA-4): instalable es criterio ADR §6 y cuesta S; se adelanta a B7.
7. **Notificaciones**: desktop foreground (S, sobre SSE existente) separada de Web Push (L); la primera se adelanta a C5.
8. **Mecanismos G3 sin regla de orden** — añadida a G3 y aplicada: `retainedInViewIds` CON la vista no-leídos (A3), `batched()` ANTES de acciones de hilo (B1), JSON-Pointer ANTES de labels (A5→C4), cutoff de merge CON la paginación (B3), `coalesceRefresh` YA (A5 — el fan-out existe hoy).
9. **Sin tamaños ni totales** — añadidos por ítem y por fase (G6), con el total honesto de ~12-15 semanas. El dueño no puede firmar magnitudes invisibles.

### 8.3 Qué faltaba por completo — añadido

- **Botón/atajo de spam** (A3): cero ocurrencias en la PWA; ni el plan ni la tabla de brechas lo traían como fila propia. Es MOVE a Junk (servidor listo) y entrena Rspamd gratis.
- **Acciones del lector** (imprimir, ver original, marcar no-leído, mover, siguiente/anterior) — estaban en el relevamiento (§3.1) y la v1 no las asignó a ninguna fase.
- **Undo de acciones (`z`)** (B5): CORE en Gmail, inexistente en Bulwark, casi gratis sobre nuestras inverse patches. Omitido en la v1.
- **Outbox/fallos de envío visibles** (B6): fila (a)/S del relevamiento, sin fase en la v1.
- **`SearchSnippet/get`** (B4): la oportunidad de superar a ambos referentes, nombrada por el relevamiento y ausente del plan.
- **`is:starred` requiere predicado bitmask nuevo** (B4): el filtro `hasKeyword $flagged` se rechaza hoy (`query.go:882`).
- **Autocompletado de direcciones sin subsistema de contactos** (C6): ni el relevamiento ni la v1 lo separaban del XL de Contactos; sin él no hay cliente diario.
- **Profundidad de teclado** (`[`/`]`, `v`, `l`, `* a`) (D1): el relevamiento la marcaba como victoria barata sobre nuestra arquitectura; la v1 no la traía.
- **El techo de 10.000 vs ADR §6 100k vs la cuenta real de 26.869** (B3 + riesgo 6): el criterio medible que la v1 dejaba caer en silencio.
- **Sección de diferidos nombrados** (§5): snooze, schedule send, mute, plantillas, cambio de contraseña/TOTP — la v1 los omitía sin declararlos, violando su propio G5.
- **Riesgos 6-8** (techo de alcance, drift del esquema de preferencias, salud del oráculo Bulwark + advisory del v1.8.1 desplegado).
- **D4 endurecido**: ningún criterio del ADR §6 sin fila con número en el cierre.

### 8.4 Qué NO se pudo verificar — y qué incertidumbre deja

- **La caminata autenticada por la UI de Bulwark nunca ocurrió** (el relevamiento lo declara: credenciales ausentes del entorno; el deploy redirige a login y `demoMode:false`). El inventario de ajustes viene del código fuente — más completo que una caminata en lo estructural, pero **la evidencia visual y el tráfico de red observado no existen**, y cualquier comportamiento que solo emerge con sesión real (estados de carga, gating dinámico por policy del admin, el aspecto exacto que el dueño recuerda) queda sin contrastar. Mitigación operativa: la comparación de cierre de cada fase (G1) se hace con sesión real, y la primera de ellas salda esta deuda.
- **Conteo de claves i18n de Bulwark inconsistente en el propio relevamiento** (2.951 en §1 vs 3.222 en §5); la v1 citaba 3.222. Sin impacto en decisiones (el orden de magnitud 25×~3.000 vs 2×228 es lo que importa); este plan usa "~3.000".
- **Los tamaños son juicio, no medición** — en particular el XL de Sieve (plausible: tokenizer ~800 líneas + ManageSieve + builder + round-trip) y el L de conversación (plausible pero es la épica con más incógnitas de UI). G6 obliga a re-estimar al abrir cada fase.
- **Ítems de Gmail marcados `unsourced` en el relevamiento** (presets de offline 7/30/90, auto-borrado de spam a 30 días, etc.) siguen sin fuente; nada de este plan depende de ellos.
