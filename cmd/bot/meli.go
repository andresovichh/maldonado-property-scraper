package main

// MercadoLibre como fuente adicional en tiempo de consulta: si hay credenciales
// de app (MELI_APP_ID / MELI_SECRET en el env), cada búsqueda consulta
// /sites/MLU/search (categoría MLU1459 = Inmuebles) además de la base
// crawleada. Los resultados se mapean a model.Listing y pasan por el MISMO
// passes() que la base local, así zonas/exclusiones/precio aplican parejo.
// Sin credenciales el bot funciona igual que antes (solo base local).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

var (
	meliID     = os.Getenv("MELI_APP_ID")
	meliSecret = os.Getenv("MELI_SECRET")

	meliMu  sync.Mutex
	meliTok string
	meliExp time.Time
)

func meliEnabled() bool { return meliID != "" && meliSecret != "" }

// meliToken devuelve un token de app (client_credentials), cacheado hasta que
// expira. La búsqueda anónima da 403: el token es requisito de MELI.
func meliToken() (string, error) {
	meliMu.Lock()
	defer meliMu.Unlock()
	if meliTok != "" && time.Now().Before(meliExp) {
		return meliTok, nil
	}
	resp, err := httpc.PostForm("https://api.mercadolibre.com/oauth/token", url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {meliID},
		"client_secret": {meliSecret},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Message     string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("meli oauth: %s", out.Message)
	}
	meliTok = out.AccessToken
	meliExp = time.Now().Add(time.Duration(out.ExpiresIn-60) * time.Second)
	return meliTok, nil
}

// meliSearch consulta inmuebles en MLU según el spec y devuelve listings
// normalizados. Errores → nil (la búsqueda local nunca se cae por MELI).
func meliSearch(sp *spec) []*model.Listing {
	if !meliEnabled() {
		return nil
	}
	tok, err := meliToken()
	if err != nil {
		log.Printf("meli token: %v", err)
		return nil
	}
	var terms []string
	switch sp.PropertyType {
	case "house":
		terms = append(terms, "casa")
	case "apartment":
		terms = append(terms, "apartamento")
	case "land":
		terms = append(terms, "terreno")
	case "chacra":
		terms = append(terms, "chacra")
	case "field":
		terms = append(terms, "campo")
	case "commercial":
		terms = append(terms, "local")
	}
	if sp.Operation == "sale" {
		terms = append(terms, "venta")
	} else if strings.HasPrefix(sp.Operation, "rent") {
		terms = append(terms, "alquiler")
	}
	terms = append(terms, sp.Zones...)
	if len(sp.Zones) == 0 {
		terms = append(terms, "maldonado")
	}
	params := url.Values{
		"category": {"MLU1459"},
		"q":        {strings.Join(terms, " ")},
		"limit":    {"50"},
	}
	if sp.PriceMin > 0 || sp.PriceMax > 0 {
		lo, hi := "*", "*"
		if sp.PriceMin > 0 {
			lo = strconv.FormatFloat(sp.PriceMin, 'f', 0, 64)
		}
		if sp.PriceMax > 0 {
			hi = strconv.FormatFloat(sp.PriceMax*1.25, 'f', 0, 64) // mismo margen que passes()
		}
		params.Set("price", lo+"-"+hi)
	}
	req, _ := http.NewRequest("GET", "https://api.mercadolibre.com/sites/MLU/search?"+params.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := httpc.Do(req)
	if err != nil {
		log.Printf("meli search: %v", err)
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Results []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Price      float64 `json:"price"`
			CurrencyID string  `json:"currency_id"`
			Permalink  string  `json:"permalink"`
			Location   struct {
				Neighborhood struct {
					Name string `json:"name"`
				} `json:"neighborhood"`
				City struct {
					Name string `json:"name"`
				} `json:"city"`
				State struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"location"`
			Attributes []struct {
				ID        string `json:"id"`
				ValueName string `json:"value_name"`
			} `json:"attributes"`
		} `json:"results"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("meli search decode: %v", err)
		return nil
	}
	if out.Error != "" {
		log.Printf("meli search: %s", out.Error)
		return nil
	}
	var ls []*model.Listing
	for _, r := range out.Results {
		// La base es del departamento de Maldonado; MELI puede devolver de todo.
		if st := strings.ToLower(r.Location.State.Name); st != "" && !strings.Contains(st, "maldonado") {
			continue
		}
		l := &model.Listing{
			AgencyDomain: "mercadolibre.com.uy",
			SourceID:     r.ID,
			URL:          r.Permalink,
			Title:        model.Str(r.Title),
			Department:   model.Str("Maldonado"),
		}
		if c := r.Location.City.Name; c != "" {
			l.City = model.Str(c)
		}
		if n := r.Location.Neighborhood.Name; n != "" {
			l.Neighborhood = model.Str(n)
		}
		if r.CurrencyID != "" {
			l.Currency = model.Str(r.CurrencyID)
		}
		var op string
		for _, a := range r.Attributes {
			v := strings.ToLower(a.ValueName)
			switch a.ID {
			case "OPERATION":
				if strings.Contains(v, "venta") {
					op = model.OperationSale
				} else if strings.Contains(v, "alquiler") {
					op = model.OperationRentUnknown
				}
			case "PROPERTY_TYPE":
				switch {
				case strings.Contains(v, "casa"):
					l.PropertyType = model.Str(model.TypeHouse)
				case strings.Contains(v, "apartamento") || strings.Contains(v, "apto"):
					l.PropertyType = model.Str(model.TypeApartment)
				case strings.Contains(v, "terreno"):
					l.PropertyType = model.Str(model.TypeLand)
				case strings.Contains(v, "chacra"):
					l.PropertyType = model.Str(model.TypeChacra)
				case strings.Contains(v, "campo"):
					l.PropertyType = model.Str(model.TypeField)
				case strings.Contains(v, "local") || strings.Contains(v, "oficina") || strings.Contains(v, "galpón"):
					l.PropertyType = model.Str(model.TypeCommercial)
				}
			case "BEDROOMS", "DORMITORIOS":
				if n, err := strconv.Atoi(strings.Fields(a.ValueName)[0]); err == nil {
					l.Bedrooms = model.Int(n)
				}
			case "FULL_BATHROOMS", "BATHROOMS":
				if n, err := strconv.Atoi(strings.Fields(a.ValueName)[0]); err == nil {
					l.Bathrooms = model.Int(n)
				}
			}
		}
		if op != "" {
			l.Operation = model.Str(op)
		}
		if r.Price > 0 {
			if op == model.OperationSale {
				l.SalePrice = model.Float(r.Price)
			} else {
				l.Price = model.Float(r.Price)
			}
		}
		ls = append(ls, l)
	}
	return ls
}
