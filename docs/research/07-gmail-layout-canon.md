# 07 — The Gmail layout canon: spatial IA verified against the live product

> **Status:** research / evidence base for epic E12 (muscle-memory parity).
> **Date:** 2026-08-31 · **Method:** authenticated walkthrough of Gmail's live web UI (the owner's session, es-419 locale, owner-authorized), captured with Playwright. Screenshots are **local evidence only** (they contain real mail) — they live outside the repo; this document is the committed description. Owner's mandate: *"la mayoría de las personas saben usar Gmail con los ojos cerrados; debemos ser lo más similares posible para que nadie necesite un manual."*
> **Why this document exists:** canon 06 verified Gmail's *behaviors* from documentation. Google documents no layout — so the spatial IA went unverified and Moov shipped Gmail semantics with its own layout. That is a soft repeat of the original method error. This canon closes it: **for UX, Gmail also defines the WHERE.**
> **Filter (inherited from canon 06 §1):** mirror what carries muscle memory; skip what exists for Google's ecosystem (app grid, Chat/Meet/Gemini, Workspace chrome, ads); never degrade Moov's signed security/honesty decisions.

## 1. Top bar (one row, full width)

Left → right: **hamburger** (collapses the left rail to icons) · product logo+name · **search box, centered-left, pill-shaped, wide** (~40% of viewport) with the search icon inside-left and the **advanced-search sliders icon inside-right** · right cluster: help `?` · **settings gear — THE settings entry, top-right** · account avatar (initial disc). Google-only, skipped: status chip, "Mejora…" upsell, Gemini, app grid, Chat/Meet/Meet rails, right-side app rail (Calendar/Keep/Tasks).

## 2. Left rail

Top: **"Redactar" as a large rounded pill with a pencil icon** — the most prominent control on the page. Below, the folder list: icon + name + right-aligned count per row; active row has a filled tint and bold text. Order in Gmail: Recibidos, Destacados, Pospuestos, Importantes, Enviados, Borradores, [categories], Más (collapse). Moov's applicable order: Recibidos, Destacados, Pospuestos, Enviados, Borradores, Programados/Salida cuando no vacíos, Más → (Archivo, Spam, Papelera, resto). Below: **"Etiquetas" section header with a `+` button** on the right; label rows with color swatch. The rail collapses to icon-only via the hamburger.

## 3. Message list

- **Toolbar row** above the list: select-all checkbox **with dropdown arrow** (Todos/Ninguno/Leídos/No leídos/Destacados/Sin destacar) · refresh · `⋮` more. Right side of the same row: **pagination "1–50 de 15.224" with `‹` `›` arrows**.
- **Row anatomy, left → right:** checkbox · **star (clickable, outline→filled)** · [importance marker — AI-gated, Moov: IA phase] · sender, bold when unread, with thread count ("yo, Daniela 10") · subject (bold when unread) + " - " + snippet in gray, one line, truncated · **attachment chips** on a second line when present (icon + filename, clickable) · right-aligned date/time (time for today, "27 ago" otherwise). Unread rows: white background + bold; read rows: subtly tinted background.
- **Hover:** row elevates (shadow), date is replaced by **4 icon actions right-aligned: archivar, eliminar, marcar leído/no-leído, posponer** (canon 06 §2.2 confirmed visually).
- Category tabs (Principal/Promociones/Social/Notificaciones) with icons and "N nuevos" badges — **IA phase** (canon 06 §4.1.4); the tab STRIP's design is recorded for that phase.

## 4. Quick settings ("Ajustes rápidos") — the gear's target

Clicking the gear opens a **docked panel from the right edge** (the list SHRINKS to make room — no overlay, no modal, page stays interactive). Anatomy: header "Ajustes rápidos" + close X · **"Ver todos los ajustes" as a full-width outlined button at the top** · then sections, each a label + radio/thumbnail options **with visual previews** (small line-art thumbnails of what the option looks like): Densidad (Predeterminada/Cómoda/Compacta, each with a mini list preview) · Tema (thumbnail + "Ver todo") · Tipo de bandeja de entrada (radios with mini-previews; "Personalizar" links under applicable ones) · Panel de lectura (Sin división / A la derecha de la bandeja / Debajo de la bandeja, with previews). Moov maps: densidad, tema (3 thumbnails), tipo de bandeja (Predeterminada/No leídos primero/Destacados primero), panel de lectura — all already server-backed prefs.

## 5. Full settings — a PAGE, not a dialog

"Ver todos los ajustes" navigates to a **settings page** that replaces the list area (top bar and left rail stay). Anatomy: title "Configuración" · **horizontal tab row** (text tabs, active = blue underline): General, Etiquetas, Recibidos, Cuentas e importación, Filtros y direcciones bloqueadas, Reenvío y correo POP/IMAP, Complementos, Chat y Meet, Avanzadas, Sin conexión, Temas · content = **two-column rows**: bold setting name (+optional gray footnote) in a ~340px left column; controls (radios with inline explanations, dropdowns, previews) right. Confirmed present on General: Idioma · **Tamaño máximo de página ("Mostrar [50▾] conversaciones por página")** · Deshacer envío ("[5▾] segundos") · Forma predeterminada de respuesta · Acciones de cursor (hover) · **Enviar y archivar (mostrar/ocultar radios)** · Estilo de texto con preview · Imágenes (siempre/preguntar) · Vista de conversación · [AI rows — skipped]. Moov's tab set: General, Etiquetas, Recibidos, Cuenta, Filtros, Bloqueados (Gmail folds them into Filtros — either is defensible; keep Gmail's fold: "Filtros y direcciones bloqueadas"), Reenvío, Sin conexión. No IMAP/POP (GC-9), no Complementos/Chat/Temas-photos. Moov's settings search (D-5) lives in the page header — an addition, placed unobtrusively.

## 6. Reading view

Opening a message replaces the list (default "Sin división"). Toolbar on top (back arrow ←, archive, report spam, delete, mark unread, snooze, move, labels, ⋮), pagination arrows remain top-right. Subject as H2 with category chip; each message: avatar disc, sender + address, date right with star and reply icons; body full-width; **the reading-pane split modes (right/below) carry a draggable divider** (E12 adds ours). Attachment cards at bottom.

## 7. Composer

**Floating card bottom-right** (not a modal): header bar with "Mensaje nuevo" + minimize/expand/close; Para/Cc/Bcc collapsed into "Para" with CC/CCO links right; subject line; body; bottom toolbar: **Enviar (blue pill, left)** with dropdown, formatting toggle, attach, link, emoji, drive[skip], image, confidential[skip], signature, ⋮ (incl. plain-text), trash-discard right. Moov currently uses a centered modal `<dialog>` — E12 decision: adopt the bottom-right floating card (the muscle-memory element: Enviar bottom-left of the card, close top-right, minimize to bar).

## 8. Search

Focus on the box → dropdown with **suggestions** (contacts with avatars, recent searches, operator completions). After Enter → results list with **chip row** under the search box (De, Cualquier fecha, Tiene archivo adjunto, No leídos, Para, [Más filtros]). The sliders icon → **advanced panel dropping from the search box** (not a separate page): fields De/Para/Asunto/Contiene las palabras/No contiene/Tamaño/Rango de fechas/Buscar en + buttons "Crear filtro" and "Buscar" bottom-right. Moov: same panel shape; "Crear filtro" wired to our filter builder.

## 9. What Moov deliberately does NOT mirror (filter applied)

App grid, Gemini/AI chrome, Chat/Meet, right app rail, upsell chips, themes-from-photos, ads, Workspace account chrome, Complementos tab, POP/IMAP tab (GC-9), dynamic-mail row, smart-features rows (IA phase, with consent), importance marker (IA phase), category tabs (IA phase — strip design recorded above for then).

## 10. E12 checklist (each item gates on a side-by-side screenshot)

1. Gear → top-right; opens quick-settings docked panel (§4) with visual previews.
2. Full settings = routed page with horizontal tabs (§5), two-column rows.
3. Redactar pill top-left of rail (§2); rail counts right-aligned; Etiquetas header with +.
4. Row anatomy per §3 incl. **clickable star** and attachment chips; hover swap date→actions (already 4).
5. Pagination "1–50 de N" + arrows in the toolbar row (§3), Gmail-form.
6. **Draggable divider** between list and reading pane (right/bottom modes), keyboard-operable, persisted.
7. Composer as floating bottom-right card (§7) with Enviar bottom-left.
8. Search: suggestion dropdown + chip row + advanced panel from the box (§8).
9. Toolbar: select-all dropdown variants (§3).
