# Spec L2 — PWA de Moov Mail (fase 3: la cara del producto)

> **Estado:** ACEPTADA — decisiones de producto tomadas por Diego (2026-08-25); arbitrajes técnicos firmados por el director bajo la delegación vigente
> **Autor:** Fable 5 (director técnico) · **Nivel:** L2
> **Base:** ADR-001 §3 y §6 (stack y criterios Gmail-class medibles) · §5 (seguridad HTML de 3 capas) · L2-jmap-server + L2-jmap-write (la API que consumimos, terminada y probada en producción por Bulwark)
> **Hito de cierre:** la PWA de Moov reemplaza a Bulwark en el piloto, con marca por dominio, y Diego la usa como cliente diario.

---

## 1. Objetivo

Construir el frontend propio de Moov: una PWA React/TypeScript que consume nuestra API JMAP y alcanza los criterios Gmail-class del ADR §6 — con **marca personalizable por dominio**, que es a la vez requisito de producto y palanca comercial (white-label para el parque instalado de Mailcow).

**No-scope:** calendario/contactos/drive (proyectos aparte, después de la fase de IA); administración web de cuentas (hoy `moovctl`); fase 3 del protocolo (Sieve, cuotas).

## 2. Decisiones de producto (fijadas por Diego)

**P1 — Login de un solo paso** (usuario + contraseña juntos). Google y Microsoft usan dos pasos por *identity-first routing* (ruteo a SSO corporativo, passkeys, multi-cuenta) — beneficios que hoy no tenemos: nuestra auth es un LOGIN IMAP contra Dovecot y el dominio de la URL ya identifica a la empresa. Copiar la forma sin la función sería agregar fricción gratis. Fastmail y Superhuman (nuestra vara real, no Google) usan un solo paso. **El componente debe diseñarse para poder partirse en dos pasos** cuando lleguen SSO/passkeys, sin rehacerlo.

**P2 — Pantalla partida (split screen).** Mitad imagen/marca, mitad formulario. Google y Microsoft NO la usan porque sirven a miles de millones de usuarios sin contexto compartido: no hay imagen que signifique algo para todos. Moov es el caso opuesto — cada instalación sirve a UNA empresa por dominio, que es exactamente el patrón donde el split screen es canónico (CRMs, sistemas de gestión B2B). Requisitos: en móvil colapsa (la imagen pasa a fondo con overlay legible o se oculta); el formulario nunca queda por debajo del pliegue.

**P3 — Marca por dominio, con defaults de Moov.** El navegador entra por `mail.<dominio>` y la PWA muestra **logo** y **imagen del panel** de esa empresa. Si el cliente no personalizó nada, se muestran los de Moov. Aplica a toda la app (no solo al login): sidebar, pestaña del navegador, ícono de la PWA instalada, pantallas vacías.

## 3. Arbitrajes técnicos

**W-A1 — El branding se sirve desde el servidor, sin autenticación, resuelto por Host.** Un endpoint público `GET /branding` (en `internal/jmaphttp`) responde según el `Host` de la petición: `{name, logoUrl, splashUrl, colors{...}}`, con los valores de Moov como fallback. Razones: (a) la marca debe verse ANTES del login (es el login), así que no puede depender de auth; (b) resolver por Host y no por lo que tipea el usuario evita filtrar qué dominios existen; (c) un solo mecanismo alimenta toda la app. Cachéable, sin datos sensibles. Configuración inicial por archivo/volumen (`/etc/moov/branding/<dominio>/`), con `moovctl branding set` como CLI — una UI de administración es fase posterior.

**W-A2 — Sistema de tokens de diseño desde el día 1.** Ningún componente hardcodea color, logo o nombre: todo sale de tokens CSS que el branding rellena en el arranque. Es barato ahora e imposible de retrofitear después. Un tema oscuro de primera clase (ADR §6) sale del mismo mecanismo.

**W-A3 — Stack: React + TypeScript + Vite, sin framework de UI pesado.** Router liviano, estado con una store mínima, IndexedDB para offline, virtualización propia o `@tanstack/virtual` para listas de 100k+. Cliente JMAP: **propio y tipado** (nuestra API está terminada y conocemos su superficie exacta; una librería genérica nos ataría a sus supuestos). Sin dependencia de componentes visuales de terceros: la identidad visual es el producto.

**W-A4 — Seguridad del render de mail: las 3 capas del ADR §5, sin excepción.** El servidor ya sanitiza (pendiente de implementar server-side en su épica), el cliente sanitiza con DOMPurify, y el HTML se renderiza en `<iframe sandbox>` sin `allow-scripts` ni `allow-same-origin`, con CSP `default-src 'none'`. Imágenes remotas bloqueadas por defecto. **Esta épica es de modelo Fable** — un bypass acá es robo de la sesión de correo completa.

## 4. Épicas

| # | Épica | Modelo | ACs clave |
|---|---|---|---|
| P1 | **Fundaciones + branding + login** | Opus | Proyecto Vite/TS con CI; sistema de tokens (W-A2); endpoint `/branding` + `moovctl branding set`; **login split-screen con marca por dominio y fallback Moov**, un paso, accesible (teclado, lectores de pantalla, contraste AA); sesión persistente; responsive real (móvil colapsa el split) |
| P2 | **Lectura** | Opus (+ **Fable** para el render seguro) | Lista virtualizada fluida con 100k+; threads; lector con las 3 capas de seguridad (W-A4, Fable); búsqueda as-you-type <100 ms percibidos; navegación de carpetas; atajos Gmail (`j/k`, `/`, `e`, `#`, `r`, `c`) |
| P3 | **Escritura y envío** | Opus | Compositor (texto+HTML, adjuntos vía upload), borradores, respuesta/reenvío con cita, **undo send** visible, acciones optimistas <100 ms con rollback, firma desde Identity |
| P4 | **Offline + PWA + pulido** | Opus | Service worker, IndexedDB con los últimos N mensajes, instalable, funciona sin red para lo cacheado; **errores accionables** (lección del piloto: "no estás habilitado, contactá al admin", nunca "ocurrió un error"); estados vacíos y de carga con marca; despliegue reemplazando a Bulwark |

Orden: P1 → P2 → P3 → P4. El piloto mantiene Bulwark hasta P4 (rollback de una línea de Caddy).

## 5. Contratos y consumo de la API

- La PWA **solo** habla JMAP estándar contra nuestro servidor: si necesita algo que la API no da, se agrega al servidor como método conforme, jamás un endpoint ad hoc.
- Gaps conocidos que P2/P3 encontrarán (ya documentados): `EmailSubmission/query` no registrado; `unsupportedFilter` en algunos conteos; `Identity` completo llegando en su épica. Se cierran en el servidor con su test, con el mismo playbook que J4/W4b.
- La PWA es un cliente más: **Bulwark debe seguir funcionando** contra el mismo servidor durante todo el desarrollo (es nuestro oráculo de regresión).

## 6. Riesgos

1. **El pulido es el 80% de la percepción** — "funciona" llega rápido; "se siente Gmail-class" no. Mitigación: Diego usa cada épica apenas aterriza y su fricción es requisito.
2. Render de mail hostil (la superficie de ataque más grande del producto) — mitigado por W-A4 y el modelo Fable.
3. Virtualización con 26k+ mensajes reales: se prueba contra la cuenta real desde P2, no con datos sintéticos.
4. Branding con activos que el cliente sube (imágenes) — validación de tipo/tamaño, servidas desde nuestro dominio, sin SVG sin sanitizar.

## 7. Aprobación

- [x] P1-P3 (producto) decididos por Diego 2026-08-25; W-A1..W-A4 firmados por el director bajo la delegación vigente.
