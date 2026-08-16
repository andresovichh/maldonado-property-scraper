# Maldonado Property Scraper

Buscador de casas en **alquiler anual** en Maldonado / Punta del Este, alimentado por
el inventario que publican **las inmobiliarias zonales**, no por los agregadores
(MercadoLibre, InfoCasas, Gallito).

La idea: normalizar publicaciones de ~150 inmobiliarias en una base común y buscar con
**scoring** en lugar de filtros rígidos, para que una casa de USD 3.100 con dependencia
y losa radiante pueda rankear por encima de una de USD 2.700 sin nada de eso.

## Estado

Milestone 1 (**scanner de tecnología**) implementado. Todavía no hay crawler, ni base
de datos, ni scoring — a propósito: primero medimos cómo están construidos los sitios,
después decidimos cuántos scrapers hacen falta.

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

Un solo adaptador cubre a toda la familia.

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

## Próximos pasos

1. **Adaptador TERA** — recorrer `/casas/en-alquiler/` paginado, parsear las tarjetas,
   seguir a `/Casa/{id}` por el detalle. Cubre la familia entera de una.
2. **PostgreSQL + JSONB** — guardar `raw` intacto siempre, para poder reprocesar sin
   volver a scrapear cuando cambie el normalizador.
3. **Normalización** — dependencia de servicio, losa radiante (incluido "**loza**
   radiante", que aparece mal escrito muy seguido), baños vs toilettes.
4. **Scoring** — perfil de búsqueda con pesos configurables, sin descartes duros salvo
   temporada vs alquiler anual.
5. **Deduplicación** — la misma casa publicada por varias inmobiliarias.

## Tests

```bash
go test ./...
```

Cubren el parseo de la nómina, la clasificación de motores (incluida la regla de señal
débil) y la heurística de inventario vs prosa.
