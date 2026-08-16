# Maldonado Property Scraper

Buscador de casas en **alquiler anual** en Maldonado / Punta del Este, alimentado por
el inventario que publican **las inmobiliarias zonales**, no por los agregadores
(MercadoLibre, InfoCasas, Gallito).

La idea: normalizar publicaciones de ~150 inmobiliarias en una base común y buscar con
**scoring** en lugar de filtros rígidos, para que una casa de USD 3.100 con dependencia
y losa radiante pueda rankear por encima de una de USD 2.700 sin nada de eso.

## Estado

- **Milestone 1 — scanner de tecnología** ✅ `./cmd/scanner`
- **Milestone 2 — adaptador TERA** ✅ `./cmd/crawl`
- **Milestone 4 — normalización** ✅ `internal/normalize`
- **Milestone 5 — búsqueda con scoring** ✅ `./cmd/search`
- **Milestone 3 — PostgreSQL** ⬜ todavía en JSON

Pipeline completo:

```bash
go run ./cmd/scanner    # descubre inmobiliarias y clasifica su tecnología
go run ./cmd/crawl      # scrapea la familia TERA → out/listings.json
go run ./cmd/search     # rankea contra el perfil de búsqueda
```

## El hallazgo que ordena el proyecto

La hipótesis del handoff era que muchas inmobiliarias comparten infraestructura.
**Se confirmó, y son dos cosas distintas que conviene no mezclar:**

| | Qué es | Huella |
|---|---|---|
| **TERA CRM** (`tera.com.uy`) | el **backend inmobiliario** que tiene el inventario | `var inmToken='…'`, `tera.uy/bot/res/iabotjs`, imágenes en `ri.com.uy` |
| **Sierra Soluciones** (`sierra.com.uy`) | el **estudio que desarrolla** los sitios | link "Desarrollado por Sierra" en el footer |

Sierra construye sitios que corren sobre TERA. Son ejes independientes: el scraper se
escribe contra **TERA**, no contra Sierra.

Y lo más importante — toda la familia comparte el **mismo esquema de URLs**:

```
/{tipo}/en-{operación}/     →  /casas/en-alquiler/   /apartamentos/en-venta/
/{Tipo}/{id}                →  /Casa/1271            (ficha)
```

El listado viene **server-side**, sin JavaScript. La tarjeta tiene siempre la misma forma:

```html
<div class="single_property_style property-1">
  <span class="property-type">COD. #1271</span>
  <h4 class="listing-name"><a href="/Casa/1271">Punta Piedras</a></h4>
  <h4 class="list-pr">U$S 50,000</h4>
  <div class="listing_features_infometas">
    <li>3 Dormitorios</li><li>4 Baños</li><li>380 m²</li>
  </div>
</div>
```

### Pero el markup NO se comparte

Sobre 40 sitios TERA muestreados aparecieron **37 templates de tarjeta distintos**:
Sierra hace diseño a medida por cliente. Lo que sí comparten es la **navegación**
(32/40 usan `/{Tipo}/{id}`) y los **rótulos de campo**.

Por eso el adaptador **navega por URL y extrae por etiqueta, nunca por selector CSS**.
Un parser de tarjetas habría que reescribirlo por inmobiliaria; uno de etiquetas no.

En las fichas no hay ld+json ni tablas, pero **todas emiten Open Graph** y todas
rotulan "Dormitorios", "Baños" y "Precios de Alquiler".

## Uso

```bash
go run ./cmd/scanner                          # nómina completa de CIPEM
go run ./cmd/scanner -limit 10                # prueba rápida
go run ./cmd/scanner -domains gary.uy,aispuru.com
```

Salida en `out/scan.json` y `out/scan.csv`, más un resumen por consola.

## Resultado de la primera corrida (2026-08-16)

```
156 inmobiliarias

  tera            69      alcanzables    141
  custom          55      sin responder   15
  unknown         15      con inventario  86
  wordpress       14      requiere JS      4
  wix              3

cobertura por adaptador (inmobiliarias con inventario server-side):
  tera            67
  custom          11
  wordpress        7
  wix              1
```

**Un solo adaptador (TERA) cubre 67 de las 86 inmobiliarias con inventario usable: 78%.**
Ése es todo el argumento para no escribir 100 scrapers.

De las 69 clasificadas como TERA, **60 por señal fuerte** y 9 confirmadas por el esquema
de URLs. 60 sitios están desarrollados por Sierra. Sólo 4 sitios en toda la nómina
requieren JavaScript, así que Rod/Chromium queda para el final, si es que hace falta.

Los 15 "sin responder" son honestos: 9 socios no tienen web en la nómina, 4 no resuelven
DNS y 2 rechazan la conexión.

Flags útiles: `-workers`, `-delay` (por host), `-timeout`, `-deadline`.

## La trampa del precio

Las fichas traen una tabla **"Precios de Alquiler" por período**:

```
Precios de Alquiler
  2da Quincena Enero    U$S 17.000
  1er Quincena Febrero  U$S 12.000
  Anual en Dólares      U$S  7.500
```

Los primeros son **temporada** — una quincena de verano, no un alquiler mensual.
Confundirlos sería lo peor que podría hacer este scraper, así que la operación sale
de **qué fila existe**, no de la URL bajo la que estaba el aviso. Muchos avisos que
viven en `/casas/en-alquiler/` son temporada pura.

Y el detalle que rompe todo si no se ve: **`Anual: U$S 0` significa "no se alquila
anual"**, no una casa gratis. Leído literal es la casa más barata de Maldonado y
encabeza cualquier ranking. Cero se trata como desconocido
(`TestAnnualPriceIgnoresZeroSentinel`).

Lo mismo con `Superficie 0 m²`, que es el "sin cargar" de la familia.

## Precio: decaimiento suave, no escalones

El handoff pedía dos cosas incompatibles: penalidades escalonadas (−5 pasando la
banda, −12 más arriba) **y** que una casa de USD 3.100 con dependencia y losa
rankee sobre una de USD 2.700 sin nada.

No pueden convivir. Cruzar los 3.000 costaba 35 puntos de una (perdías el +30 de
banda y sumabas −5), y ninguna combinación de features lo compensa — es un filtro
duro disfrazado de blando. Ganó tu ejemplo: el precio ahora **decae linealmente**
desde el tope de la banda hasta el máximo. Está fijado en
`TestBetterHouseOverBudgetBeatsCheaperBareOne`.

También hay un piso de plausibilidad: apareció un aviso real con
`Alq. Anual (Dólares): USD 200` para una casa de 3 dormitorios. No se oculta —
se muestra con el motivo escrito, pero deja de puntuar como ganga.

## Cómo clasifica

Dos niveles de evidencia, a propósito:

- **Señal fuerte** — `inmToken`, el script de `tera.uy/bot`, o el link a `tera.com.uy`.
  Alcanza por sí sola.
- **Señal débil** — imágenes servidas desde `ri.com.uy`. Es un CDN compartido por más
  sistemas que TERA, así que **sola no clasifica**: se promueve a `tera` únicamente si
  además responde el esquema `/casas/en-alquiler/`.

`gary.uy` es el caso que motivó la regla: matcheaba el CDN pero había que confirmarlo
por URL. Está fijado en `TestRiComUyAloneIsNotTera`.

Para decidir si hay inventario, una página necesita **dos** señales independientes
(precios **y** cantidad de dormitorios/baños). Con una sola, un "vendimos USD 5.000.000
en 2026" contaría como listado.

## Buenos modales

- `User-Agent` propio e identificable, sin hacerse pasar por browser.
- Una request por vez **por host**, con delay configurable (default 900 ms).
- `robots.txt`: un `Disallow: /` que nos aplique corta el sondeo de ese sitio.
- Cuerpo limitado a 3 MiB; timeouts en todos lados.
- No se toca nada privado, ni autenticación, ni CAPTCHA. Sólo páginas públicas.

Se prefiere siempre un endpoint JSON que ya use el frontend antes que renderizar HTML,
y renderizar HTML antes que levantar un browser.

## Estructura

```
cmd/scanner/            el inventario tecnológico (Milestone 1)
internal/discovery/
  cipem.go              nómina de socios de CIPEM → lista de inmobiliarias
  detect.go             clasificación de motor + búsqueda de listados/API
  fetch.go              cliente HTTP educado (serializa por host)
```

CIPEM es el seed registry: `https://cipem.org.uy/socios/nomina/`.
**Ojo:** el dominio es `cipem.org.uy`, no `cipem.com.uy` (ése no resuelve).

Las carpetas de `scraper/`, `normalize/`, `scoring/`, `storage/` del plan original
todavía no existen. Se crean cuando haya datos que las justifiquen.

## Resultado del primer crawl (2026-08-16)

```
611 listings de 47 inmobiliarias TERA

  rent_annual   360      en banda USD 2.000–3.000   107
  rent_season   206      dependencia confirmada     169
  sin operación  45      losa radiante confirmada    35
```

Top del ranking con el perfil por defecto:

```
SCORE PRECIO  DORM BAÑOS LOSA SERV  INMOBILIARIA
100%  2200    3    3     sí   sí    adrianamartino.com
86%   2800    3    3     ?    sí    javiersena.com
86%   2400    3    3     ?    sí    marytierra.com.uy
83%   2500    4    3     sí   sí    inmobiliariagorlero.com
```

Un dato para calibrar expectativas: la losa radiante aparece confirmada en **35 de 360**
avisos anuales. No es que no la tengan — es que **no la mencionan**. Por eso el ranking
sólo suma cuando el aviso lo dice y nunca penaliza el silencio: `?` no es `no`.

## Próximos pasos

1. **PostgreSQL + JSONB** — hoy la salida es JSON. El `raw` ya se preserva entero, así
   que migrar es mover el sink, no rehacer el pipeline.
2. **Paginación** — los índices devuelven ~18 fichas y no se encontró link de "página
   siguiente"; falta confirmar si eso es todo el inventario o hay más detrás de un
   parámetro.
3. **Números escritos con letra** — "consta de cuatro dormitorios" no se parsea. Es
   frecuente en las descripciones largas.
4. **Las otras familias** — WordPress (7) y custom (11) suman 18 inmobiliarias más.
5. **Deduplicación** — la misma casa publicada por varias inmobiliarias.

## Tests

```bash
go test ./...
```

Cubren el parseo de la nómina, la clasificación de motores (incluida la regla de señal
débil) y la heurística de inventario vs prosa.
