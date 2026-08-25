// Command mercadolibre crawls MercadoLibre's public inmuebles listings for
// Maldonado through the Zyte API (ML blocks datacenter IPs; Zyte with
// geolocation UY pasa). Cada página de listado trae ~48 "polycards" en un JSON
// embebido (nordic ctx) con título, precio, dormitorios/baños/m² y ubicación —
// alcanza el listado, sin visitar cada aviso. Output: out/listings-meli.json
// (el bot lo levanta solo por el glob out/listings*.json).
//
// Env: ZYTE_API_KEY. Nota: ML capea cada búsqueda en ~42 páginas (~2000
// avisos); si una sección da exactamente el tope se loguea.
// ponytail: sin segmentación por precio para superar el tope; agregar bandas
// de precio si alguna sección lo pega seguido.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

const listadoBase = "https://listado.mercadolibre.com.uy"

var sections = []struct {
	path      string // sección de listado (página 1)
	operation string
	ptype     string
}{
	{"/inmuebles/casas/venta/maldonado/", model.OperationSale, model.TypeHouse},
	{"/inmuebles/apartamentos/venta/maldonado/", model.OperationSale, model.TypeApartment},
	{"/inmuebles/terrenos/venta/maldonado/", model.OperationSale, model.TypeLand},
	{"/inmuebles/casas/alquiler/maldonado/", model.OperationRentAnnual, model.TypeHouse},
	{"/inmuebles/apartamentos/alquiler/maldonado/", model.OperationRentAnnual, model.TypeApartment},
}

var (
	zyteKey  string
	nordicRe = regexp.MustCompile(`_n\.ctx\.r=({.*?});_n\.ctx\.r\.assets\.manifest`)
	dormRe   = regexp.MustCompile(`(\d+)\s*dormitorio`)
	banoRe   = regexp.MustCompile(`(\d+)\s*baño`)
	m2Re     = regexp.MustCompile(`([\d.]+)\s*m²`)
	perPage  = 48
	hc       = &http.Client{Timeout: 90 * time.Second}
	reqCount int
)

func main() {
	var (
		outFile  = flag.String("out", "out/listings-meli.json", "output JSON")
		delay    = flag.Duration("delay", 800*time.Millisecond, "delay between pages")
		maxPages = flag.Int("max-pages", 0, "cap de páginas por sección (0 = todas)")
	)
	flag.Parse()
	zyteKey = os.Getenv("ZYTE_API_KEY")
	if zyteKey == "" {
		fmt.Fprintln(os.Stderr, "falta ZYTE_API_KEY")
		os.Exit(1)
	}

	seen := map[string]bool{}
	var all []*model.Listing
	for _, sec := range sections {
		pageCount := 1
		for page := 1; page <= pageCount; page++ {
			url := listadoBase + sec.path
			if page > 1 {
				url += fmt.Sprintf("_Desde_%d_NoIndex_True", perPage*(page-1)+1)
			}
			ist, err := fetchState(url, sec.path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s p%d: %v (sigo)\n", sec.path, page, err)
				continue
			}
			if page == 1 {
				pageCount = ist.Pagination.PageCount
				if *maxPages > 0 && pageCount > *maxPages {
					pageCount = *maxPages
				}
				if ist.Pagination.PageCount >= 42 {
					fmt.Fprintf(os.Stderr, "%s: %d páginas = tope de ML, puede haber más avisos\n", sec.path, ist.Pagination.PageCount)
				}
			}
			for _, r := range ist.Results {
				pc := r.Polycard
				if pc.Metadata.ID == "" || seen[pc.Metadata.ID] {
					continue
				}
				seen[pc.Metadata.ID] = true
				all = append(all, toListing(pc, sec.operation, sec.ptype))
			}
			fmt.Fprintf(os.Stderr, "%s p%d/%d (%d avisos)\n", sec.path, page, pageCount, len(all))
			time.Sleep(*delay)
		}
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "cero avisos: no escribo")
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(all, "", " ")
	if err := os.WriteFile(*outFile, b, 0644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("%d avisos → %s (%d requests a Zyte)\n", len(all), *outFile, reqCount)
}

// ─── Zyte fetch + estado embebido ───────────────────────────────────────

type initialState struct {
	Results []struct {
		Polycard polycard `json:"polycard"`
	} `json:"results"`
	Pagination struct {
		PageCount    int `json:"page_count"`
		SelectedPage int `json:"selected_page"`
	} `json:"pagination"`
}

type polycard struct {
	Metadata struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"metadata"`
	Components []struct {
		Type  string `json:"type"`
		Title *struct {
			Text string `json:"text"`
		} `json:"title"`
		Price *struct {
			CurrentPrice struct {
				Value    float64 `json:"value"`
				Currency string  `json:"currency"`
			} `json:"current_price"`
		} `json:"price"`
		AttributesList *struct {
			Texts []string `json:"texts"`
		} `json:"attributes_list"`
		Location *struct {
			Text string `json:"text"`
		} `json:"location"`
	} `json:"components"`
}

// fetchState baja una página vía Zyte y saca el initialState del JSON nordic.
// Reintenta hasta 3 veces: ML a veces devuelve su página de verificación y el
// próximo request (otra IP de Zyte) suele pasar.
func fetchState(url, wantPath string) (*initialState, error) {
	var lastErr error
	for try := 1; try <= 3; try++ {
		body, _ := json.Marshal(map[string]any{
			"url": url, "httpResponseBody": true, "geolocation": "UY",
		})
		req, _ := http.NewRequest("POST", "https://api.zyte.com/v1/extract", bytes.NewReader(body))
		req.SetBasicAuth(zyteKey, "")
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		reqCount++
		var out struct {
			URL              string `json:"url"`
			HTTPResponseBody string `json:"httpResponseBody"`
			Detail           string `json:"detail"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if out.HTTPResponseBody == "" {
			lastErr = fmt.Errorf("zyte: %s", out.Detail)
			continue
		}
		if strings.Contains(out.URL, "account-verification") {
			lastErr = fmt.Errorf("verificación anti-bot")
			continue
		}
		// Redirect fuera de la sección = slug inválido (búsqueda genérica): abortar.
		if !strings.Contains(out.URL, strings.TrimSuffix(wantPath, "/")) {
			return nil, fmt.Errorf("redirigido a %s (sección inexistente)", out.URL)
		}
		html, err := base64.StdEncoding.DecodeString(out.HTTPResponseBody)
		if err != nil {
			lastErr = err
			continue
		}
		m := nordicRe.FindSubmatch(html)
		if m == nil {
			lastErr = fmt.Errorf("sin nordic ctx")
			continue
		}
		var ctx struct {
			AppProps struct {
				PageProps struct {
					InitialState *initialState `json:"initialState"`
				} `json:"pageProps"`
			} `json:"appProps"`
		}
		if err := json.Unmarshal(m[1], &ctx); err != nil {
			lastErr = err
			continue
		}
		if ctx.AppProps.PageProps.InitialState == nil {
			lastErr = fmt.Errorf("sin initialState")
			continue
		}
		return ctx.AppProps.PageProps.InitialState, nil
	}
	return nil, lastErr
}

// ─── mapeo ──────────────────────────────────────────────────────────────

func toListing(pc polycard, operation, ptype string) *model.Listing {
	l := &model.Listing{
		AgencyDomain: "mercadolibre.com.uy",
		SourceID:     pc.Metadata.ID,
		URL:          "https://" + strings.TrimPrefix(strings.TrimPrefix(pc.Metadata.URL, "https://"), "http://"),
		Operation:    model.Str(operation),
		PropertyType: model.Str(ptype),
		Department:   model.Str("Maldonado"),
	}
	for _, c := range pc.Components {
		switch {
		case c.Type == "title" && c.Title != nil:
			l.Title = model.Str(strings.TrimSpace(c.Title.Text))
		case c.Type == "price" && c.Price != nil && c.Price.CurrentPrice.Value > 0:
			l.Currency = model.Str(c.Price.CurrentPrice.Currency)
			if operation == model.OperationSale {
				l.SalePrice = model.Float(c.Price.CurrentPrice.Value)
			} else {
				l.Price = model.Float(c.Price.CurrentPrice.Value)
			}
		case c.Type == "attributes_list" && c.AttributesList != nil:
			for _, t := range c.AttributesList.Texts {
				t = strings.ToLower(t)
				if m := dormRe.FindStringSubmatch(t); m != nil {
					if n, _ := strconv.Atoi(m[1]); n > 0 {
						l.Bedrooms = model.Int(n)
					}
				}
				if m := banoRe.FindStringSubmatch(t); m != nil {
					if n, _ := strconv.Atoi(m[1]); n > 0 {
						l.Bathrooms = model.Int(n)
					}
				}
				if m := m2Re.FindStringSubmatch(t); m != nil {
					if f, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ".", ""), 64); f > 0 {
						l.BuiltM2 = model.Float(f)
					}
				}
			}
		case c.Type == "location" && c.Location != nil:
			// "Manantiales, Maldonado" → barrio, ciudad/depto
			parts := strings.SplitN(c.Location.Text, ",", 2)
			if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
				l.Neighborhood = model.Str(strings.TrimSpace(parts[0]))
			}
			if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
				l.City = model.Str(strings.TrimSpace(parts[1]))
			}
		}
	}
	return l
}
