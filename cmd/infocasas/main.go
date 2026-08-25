// Command infocasas crawls InfoCasas' Maldonado search pages into normalised
// listings. Cada página de resultados trae un JSON embebido (__NEXT_DATA__)
// con los avisos completos —precio, dormitorios, ficha técnica, barrio,
// descripción— así que no hace falta visitar cada aviso: solo se pagina el
// listado. Output: out/listings-infocasas.json (el bot lo levanta solo por
// el glob out/listings*.json).
//
//	go run ./cmd/infocasas                    # todas las secciones
//	go run ./cmd/infocasas -max-pages 5       # vistazo rápido
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

const (
	base = "https://www.infocasas.com.uy"
	ua   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

var sections = []struct {
	path      string
	operation string
	ptype     string
}{
	{"/venta/casas/maldonado", model.OperationSale, model.TypeHouse},
	{"/venta/apartamentos/maldonado", model.OperationSale, model.TypeApartment},
	{"/venta/terrenos/maldonado", model.OperationSale, model.TypeLand}, // incluye chacras (InfoCasas los agrupa)
	{"/alquiler/casas/maldonado", model.OperationRentAnnual, model.TypeHouse},
	{"/alquiler/apartamentos/maldonado", model.OperationRentAnnual, model.TypeApartment},
}

var nextDataRe = regexp.MustCompile(`__NEXT_DATA__[^>]*>([^<]+)</script>`)

func main() {
	var (
		outFile  = flag.String("out", "out/listings-infocasas.json", "output JSON")
		delay    = flag.Duration("delay", 600*time.Millisecond, "delay between page requests")
		timeout  = flag.Duration("timeout", 25*time.Second, "per-request timeout")
		maxPages = flag.Int("max-pages", 0, "cap of pages per section (0 = todas)")
	)
	flag.Parse()

	hc := &http.Client{Timeout: *timeout}
	seen := map[int64]bool{}
	var all []*model.Listing
	for _, sec := range sections {
		pages, sinNuevos := 0, 0
		for page := 1; ; page++ {
			url := base + sec.path
			if page > 1 {
				url += "/pagina" + strconv.Itoa(page)
			}
			sf, err := fetchPage(hc, url)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v (corto la sección)\n", url, err)
				break
			}
			nuevos := 0
			for _, it := range sf.Data {
				// Si la URL cae en una búsqueda genérica (slug inválido), llegan
				// avisos de todo el país: el departamento del item es la verdad.
				if len(it.Locations.State) == 0 || !strings.Contains(it.Locations.State[0].Name, "Maldonado") {
					continue
				}
				if seen[it.ID] {
					continue
				}
				seen[it.ID] = true
				all = append(all, toListing(it, sec.operation, sec.ptype))
				nuevos++
			}
			// Pasadas ~15 páginas seguidas sin nada nuevo, el resto es relleno
			// repetido: cortar ahorra cientos de requests por sección.
			if nuevos == 0 {
				if sinNuevos++; sinNuevos >= 15 {
					fmt.Fprintf(os.Stderr, "%s: 15 páginas sin avisos nuevos, corto\n", sec.path)
					break
				}
			} else {
				sinNuevos = 0
			}
			pages++
			fmt.Fprintf(os.Stderr, "%s p%d/%d (%d avisos)\n",
				sec.path, sf.PaginatorInfo.CurrentPage, sf.PaginatorInfo.LastPage, len(all))
			if page >= sf.PaginatorInfo.LastPage || (*maxPages > 0 && pages >= *maxPages) {
				break
			}
			time.Sleep(*delay)
		}
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "cero avisos: no escribo (¿anti-bot?)")
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(all, "", " ")
	if err := os.WriteFile(*outFile, b, 0644); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("%d avisos → %s\n", len(all), *outFile)
}

// ─── página de resultados ───────────────────────────────────────────────

type icItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Link        string `json:"link"`
	Description string `json:"description"`
	Price       struct {
		Amount   float64 `json:"amount"`
		Currency struct {
			Name string `json:"name"`
		} `json:"currency"`
	} `json:"price"`
	Bedrooms  any `json:"bedrooms"`
	Bathrooms any `json:"bathrooms"`
	Locations struct {
		State []struct {
			Name string `json:"name"`
		} `json:"state"`
		City []struct {
			Name string `json:"name"`
		} `json:"city"`
		Neighbourhood []struct {
			Name string `json:"name"`
		} `json:"neighbourhood"`
	} `json:"locations"`
	TechnicalSheet []struct {
		Field string `json:"field"`
		Value string `json:"value"`
	} `json:"technicalSheet"`
	Facilities []struct {
		Name string `json:"name"`
	} `json:"facilities"`
}

type icSearch struct {
	Data          []icItem `json:"data"`
	PaginatorInfo struct {
		CurrentPage int `json:"currentPage"`
		LastPage    int `json:"lastPage"`
		Total       int `json:"total"`
	} `json:"paginatorInfo"`
}

func fetchPage(hc *http.Client, url string) (*icSearch, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "es-UY,es;q=0.9")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	m := nextDataRe.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("sin __NEXT_DATA__")
	}
	var page struct {
		Props struct {
			PageProps struct {
				FetchResult struct {
					SearchFast icSearch `json:"searchFast"`
				} `json:"fetchResult"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &page); err != nil {
		return nil, err
	}
	sf := page.Props.PageProps.FetchResult.SearchFast
	if len(sf.Data) == 0 && sf.PaginatorInfo.Total > 0 {
		return nil, fmt.Errorf("página sin data")
	}
	return &sf, nil
}

// ─── mapeo a model.Listing ──────────────────────────────────────────────

var tagRe = regexp.MustCompile(`<[^>]+>`)

func toListing(it icItem, operation, ptype string) *model.Listing {
	l := &model.Listing{
		AgencyDomain: "infocasas.com.uy",
		SourceID:     strconv.FormatInt(it.ID, 10),
		URL:          base + it.Link,
		Operation:    model.Str(operation),
		PropertyType: model.Str(ptype),
		Department:   model.Str("Maldonado"),
		Title:        model.Str(strings.TrimSpace(it.Title)),
	}
	if d := strings.TrimSpace(tagRe.ReplaceAllString(it.Description, " ")); d != "" {
		l.Description = model.Str(d)
	}
	if len(it.Locations.City) > 0 {
		l.City = model.Str(it.Locations.City[0].Name)
	}
	var barrios []string
	for _, n := range it.Locations.Neighbourhood {
		barrios = append(barrios, n.Name)
	}
	if len(barrios) > 0 {
		l.Neighborhood = model.Str(strings.Join(barrios, ", "))
	}
	if it.Price.Amount > 0 {
		cur := "USD"
		if strings.Contains(it.Price.Currency.Name, "$") && !strings.Contains(it.Price.Currency.Name, "U") {
			cur = "UYU"
		}
		l.Currency = model.Str(cur)
		if operation == model.OperationSale {
			l.SalePrice = model.Float(it.Price.Amount)
		} else {
			l.Price = model.Float(it.Price.Amount)
		}
	}
	if n := anyInt(it.Bedrooms); n > 0 {
		l.Bedrooms = model.Int(n)
	}
	if n := anyInt(it.Bathrooms); n > 0 {
		l.Bathrooms = model.Int(n)
	}
	for _, ts := range it.TechnicalSheet {
		v := strings.TrimSpace(ts.Value)
		if v == "" {
			continue
		}
		switch ts.Field {
		case "bedrooms":
			if l.Bedrooms == nil {
				if n, err := strconv.Atoi(v); err == nil {
					l.Bedrooms = model.Int(n)
				}
			}
		case "bathrooms":
			if l.Bathrooms == nil {
				if n, err := strconv.Atoi(v); err == nil {
					l.Bathrooms = model.Int(n)
				}
			}
		case "m2Built":
			if f := parseM2(v); f > 0 {
				l.BuiltM2 = model.Float(f)
			}
		case "garage":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				l.Garage = model.Bool(true)
			}
		case "property_type_name":
			switch strings.ToLower(v) {
			case "casa":
				l.PropertyType = model.Str(model.TypeHouse)
			case "apartamento":
				l.PropertyType = model.Str(model.TypeApartment)
			case "terreno":
				l.PropertyType = model.Str(model.TypeLand)
			case "chacra", "campo":
				l.PropertyType = model.Str(model.TypeChacra)
			case "local comercial", "oficina", "galpón":
				l.PropertyType = model.Str(model.TypeCommercial)
			}
		}
	}
	for _, f := range it.Facilities {
		n := strings.ToLower(f.Name)
		switch {
		case strings.Contains(n, "piscina"):
			l.Pool = model.Bool(true)
		case strings.Contains(n, "parrillero"):
			l.BBQ = model.Bool(true)
		case strings.Contains(n, "garaje") || strings.Contains(n, "garage") || strings.Contains(n, "cochera"):
			l.Garage = model.Bool(true)
		case strings.Contains(n, "losa radiante"):
			l.RadiantHeating = model.Bool(true)
		case strings.Contains(n, "dependencia"):
			l.ServiceRoom = model.Bool(true)
		}
	}
	return l
}

// anyInt tolera que InfoCasas mande números como int, float o string.
func anyInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

// parseM2 parsea "490 m2" / "3.392 m2" (punto = miles).
func parseM2(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(s), "m2"))
	s = strings.ReplaceAll(strings.TrimSpace(s), ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
