# Spike S4 — Corpus de MIME patológico y validación dual-parser

> **Fecha:** 2026-08-09 · **Resultado: ✅ VALIDADO — estrategia de doble parser confirmada empíricamente**
> Objetivo: construir el corpus de MIME patológico ANTES de escribir el parser del producto (regla del proyecto; riesgo R4 del research §4.2) y validar la cascada go-message → enmime contra él.
> Corpus permanente: `testdata/mime-corpus/` (110 casos + manifest). Harness y datos crudos: `spikes/s4-mime/` (RESULTS.md).

## Qué se construyó

- **110 casos en 9 categorías** — bombas de anidamiento (hasta 500 niveles, 1000 partes hermanas), boundaries rotos, patología de headers (20 KB, 10.000 headers, NUL bytes), encoded-words RFC 2047 rotos, charset hell (declarado vs real), Content-Transfer-Encoding mentiroso, absurdos estructurales, caos de line-endings y rarezas del mundo real (TNEF, S/MIME mutilado, mbox `From ` leak).
- **`manifest.yaml` como especificación**: cada caso declara qué tiene de patológico, procedencia, licencia y qué DEBE hacer un parser correcto (`ok | partial | failed`). Las expectativas se escribieron **antes** de correr los parsers; los desacuerdos se examinaron como hallazgos, nunca se editaron para calzar (§6 de RESULTS.md documenta los 4 desacuerdos y su veredicto).
- **Generador determinístico** (`spikes/s4-mime/gen/`) para los casos tediosos — regeneración byte-idéntica verificada.
- **Harness dual-parser** (`spikes/s4-mime/`): corre los 110 casos contra `go-message` v0.18.2 y `enmime` v2.4.1 con watchdog de 10 s, recover de panics y techo de memoria; detecta el peor caso posible: datos incorrectos silenciosos.

## Matriz de resultados (titular)

| Resultado | Casos | Significado para el engine |
|---|---:|---|
| Ambos parsean | **97** | Camino normal |
| Solo go-message falla | **9** | enmime rescata |
| Solo enmime falla | **1** | go-message rescata — el fallback es **bidireccional** |
| Ambos fallan | **3** | Camino `parse_status='failed'` + blob crudo |
| Panics / cuelgues | **0 / 0** | Sin vector de DoS en 110 inputs hostiles |

## Hallazgos de ingeniería (insumos para el parser del engine)

| # | Hallazgo | Implicancia para Moov |
|---|---|---|
| H1 | **Doble parser validado**: 10/110 casos (9,1%) parseables por exactamente uno, en ambas direcciones (un `charset` duplicado voltea a enmime; headers rotos y boundaries huérfanos voltean a go-message) | Cascada **go-message → enmime → blob crudo**, probando ambos antes de declarar `failed`. Una cascada unidireccional pierde mensajes |
| H2 | **0 panics y 0 cuelgues** en 110 inputs deliberadamente hostiles | El peor caso de R4 (crash/hang que rompe el sync) no se materializa con estas librerías; el riesgo residual es **calidad de datos, no disponibilidad** |
| H3 | Los 3 both-fail son mensajes sin información estructural para dividirse (boundary vacío/inexistente, primer línea = continuación). El body sigue siendo legible como texto | El camino raw-blob es **obligatorio en fase 1**, y debe salvar el body como parte de texto única en vez de mostrar nada. enmime daña el header block al fallar — no confiar en headers parciales de un parse fallido |
| H4 | **Base64 sin padding en encoded-words: ambos parsers entregan markup crudo al usuario** (`=?UTF-8?B?…?=` como subject) **sin error ni defect flag**, aunque es unívocamente decodificable (`RawStdEncoding` lo resuelve) | Post-proceso del engine: detectar `=?…?=` residual en headers decodificados y reintentar con Raw encodings. Barato, y elimina un defecto visible al usuario |
| H5 | **Trampa de `io.ReadAll`**: go-message devuelve bytes parciales JUNTO al error; el idiomático `if err != nil { return }` los tira. El harness mismo cayó en la trampa en su primera versión | En el parse path, **nunca descartar el slice en error**: conservar lo decodificado, marcar la parte como parcial, seguir |
| H6 | **Mentiras de charset que decodifican limpio son indetectables por diseño** (windows-1252/ISO-8859-1 mapean todo byte): texto incorrecto con cero señal de error. Tablas de decode confirmadas correctas con declaraciones honestas (GB18030, KOI8-R, windows-1256) | Confirma la cascada heurística del research §4.2 (`chardet` + flag `charset_guessed`). **"Sin error de parseo" jamás significa "texto correcto"** |
| H7 | **Ningún parser desciende en `message/rfc822`**: el mensaje embebido es una hoja opaca | El contenido de mails reenviados **no se indexa para búsqueda** salvo que el engine re-parsee recursivamente — requisito de producto descubierto por el corpus. Contracara positiva: el embedding recursivo no es vector de amplificación |
| H8 | **Ningún parser tiene límite de profundidad** propio: ambos caminan 500 niveles sin inmutarse; enmime asigna ~50 KB por parte (50 MB para 1000 hermanas) | Los caps son 100% responsabilidad del engine: profundidad (~100), cantidad de partes (~1000), tamaño total; excederlos = `failed`, no "parsear más fuerte". Con árboles gigantes, preferir el streaming de go-message |
| H9 | **Line-endings**: LF-only (la forma normal de mail Unix en disco) parsea bien en ambos; CR-only no parsea en ninguno (necesitaría pre-normalización); CRLF/LF mixto diverge (enmime tolera, go-message no) | Decidir explícitamente si CR-only se soporta (pre-normalizar) o se declara `failed`; el caso mixto es otro punto para el fallback |

## Trampas de repo detectadas y corregidas

- `.gitattributes`: los `.eml` son vectores byte-exactos (CRLF vs LF es semántico en MIME) → `*.eml -text` **después** del `* text=auto` (gana la última regla). Verificado: 110/110 archivos sin conversión de eol.
- `.gitignore` tenía `*.eml` global que **ocultaba el corpus entero a git** → negación scoped `!testdata/mime-corpus/**/*.eml`.

## Riesgos abiertos

1. El corpus es sintético — codifica *nuestro* modelo de lo que se rompe. Follow-up natural: pasada read-only sobre los buzones reales del caso Crash (89 cuentas) para descubrir patologías que no imaginamos.
2. Material clásico externo (torture tests) no commiteado por licencias no establecidas; `fetch-external.sh` queda como mecanismo (sin fuentes habilitadas aún).
3. Los hallazgos son version-pinned (go-message v0.18.2, enmime v2.4.1) → el corpus debe entrar a CI desde el día 1 para atrapar regresiones en upgrades (la política del proyecto ya lo exige).

## Próximo

- **S3** (benchmark FTS 5M) es el último spike bloqueante — en ejecución al cierre de este doc.
