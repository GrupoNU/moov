# Spike S1 — JMAP sobre Dovecot/Mailcow con jmap-proxy

> **Fecha:** 2026-08-07/08 · **Resultado: ✅ VALIDADO AL 100% — incluye cliente de terceros**
> Objetivo: confirmar empíricamente que un proxy JMAP-sobre-IMAP funciona contra un Mailcow Dockerized real antes de escribir código propio.

## Setup

- Mailcow Dockerized productivo (Dovecot 2.3, red `mailcowdockerized_mailcow-network`).
- Buzón de prueba dedicado creado vía API de Mailcow (`add/mailbox`).
- Contenedor `ghcr.io/jmapio/jmap-proxy:latest` (jmap-perl) unido a la red de Mailcow, puertos publicados solo en interfaz VPN (nunca públicos).

## Qué se validó

1. **Registro de cuenta** vía management API (`POST /api/accounts`) contra `dovecot:993`.
2. **Session object** RFC 8620 en `/.well-known/jmap` con capabilities mail/quota/mdn.
3. **`Mailbox/get`**: carpetas SPECIAL-USE de Dovecot mapeadas a roles JMAP correctos (`inbox`, `sent`, `drafts`, `trash`, `junk`, `archive`), contadores `totalEmails`/`unreadEmails` correctos.
4. **Ciclo completo**: mail enviado por SMTP submission (587 STARTTLS) → entregado por Dovecot → visible vía `Email/query` + `Email/get` con **back-reference** (`#ids`/`resultOf`) en **un solo request** — el batching que los clientes JMAP reales usan idiomáticamente.

## Hallazgos de ingeniería (insumos para nuestro sync engine)

| # | Hallazgo | Implicancia para Moov |
|---|---|---|
| H1 | **jmap-proxy NO soporta IMAP STARTTLS.** `imapSSL` real en el código: 1=plano, 2=TLS implícito (el "3=STARTTLS" del README es solo para SMTP). Con 3 intenta TLS contra puerto plano → `wrong version number` | Nuestro sync engine DEBE soportar STARTTLS (dentro de la red Docker el puerto natural es 143+STARTTLS). Verificar capacidades reales en código, no en README |
| H2 | Cert interno de Dovecot es del hostname público, no del alias `dovecot` → validación de hostname falla en conexiones internas. jmap-proxy lo resuelve con `IGNORE_INVALID_CERT=1` (global, tosco) | Moov necesita opción de CA/SNI configurable por instalación o verificación con nombre esperado — no un switch global de "ignorar certs" |
| H3 | Schema de cuenta del management API: `accountid, email, type:"imap", username, password, imapHost, imapPort, imapSSL, smtpHost, smtpPort, smtpSSL` (+ `caldavURL`/`carddavURL` opcionales) | Referencia para nuestro modelo de aprovisionamiento |
| H4 | El management UI (8080) bindea a loopback del contenedor — publicar el puerto no alcanza; hay que operarlo desde dentro | En Moov: bind configurable (`MGMT_BIND`) desde el día 1 |
| H5 | La API de Mailcow rechaza requests por IPv6 interna si la allowlist es solo IPv4 → forzar IPv4 (`curl -4`) desde contenedores | Documentar en la guía de instalación de Moov |
| H6 | El proxy quedó como **referencia viva**: sirve para conectar clientes JMAP de terceros (Twake/Bulwark) y como oráculo de comportamiento del mapeo IMAP↔JMAP | Mantenerlo corriendo durante el desarrollo |
| H7 | **jmap-proxy no implementa CORS** (OPTIONS → 501, cero headers Access-Control) y Bulwark llama al servidor JMAP desde el browser → imposible en orígenes distintos. Solución: front same-origin (Caddy) ruteando `/session*`, `/jmap*`, `/.well-known/jmap*`, `/raw/*`, `/upload/*`, `/eventsource*` al proxy y el resto al webmail. Ojo: `.well-known/jmap` redirige a `/session` (ruta no obvia) | El servidor JMAP de Moov DEBE implementar CORS configurable (origins permitidos) desde el día 1 — es requisito real de los clientes web. Y documentar todas las rutas que expone la sesión |

## Cierre — cliente de terceros funcionando (2026-08-08)

**Bulwark v1.8.1** (webmail JMAP que no escribimos nosotros) desplegado detrás de un front same-origin (Caddy, ver H7) quedó **operativo contra nuestro Dovecot**: login, inbox con los 4 mails de prueba, carpetas con contadores, labels, búsqueda y shortcuts — todo viajando por JMAP estándar. Verificado por Diego en navegador (2026-08-08). Es la prueba definitiva de la tesis del proyecto: un cliente JMAP moderno arbitrario funciona sobre Mailcow cuando alguien pone el puente en el medio. Ese puente, hecho en serio, es Moov.

Stack de referencia que queda corriendo (VPN-only): Caddy (origen único) → Bulwark + jmap-proxy → Dovecot.

## Próximos spikes

- **S2:** QRESYNC/CONDSTORE/NOTIFY reales con `go-imap/v2` beta.8 contra este mismo Dovecot.
- **S3:** benchmark `tsvector` con corpus de 5M mensajes.
- **S4:** corpus de MIME patológico.
