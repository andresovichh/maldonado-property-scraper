package main

import (
	"strings"
	"testing"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

// El filtro solo descarta contradicciones CONOCIDAS: campos nil pasan.
func TestFilter(t *testing.T) {
	mu.Lock()
	listings = []*model.Listing{
		{URL: "a", Operation: model.Str(model.OperationSale), PropertyType: model.Str(model.TypeHouse),
			Price: model.Float(150000), Title: model.Str("Casa en Pinares con piscina")},
		{URL: "b", Operation: model.Str(model.OperationRentAnnual), Price: model.Float(2500)},
		{URL: "c" /* todo nil: debe pasar cualquier filtro sin zona */},
		{URL: "d", Operation: model.Str(model.OperationSale), Price: model.Float(900000)},
	}
	mu.Unlock()

	got, total := filter(&spec{Operation: "sale", PriceMax: 200000, Zones: nil})
	urls := map[string]bool{}
	for _, l := range got {
		urls[l.URL] = true
	}
	if !urls["a"] || !urls["c"] || urls["b"] || urls["d"] || total != 2 {
		t.Fatalf("filtro mal: %v (total %d)", urls, total)
	}

	// zona pedida: el nil-total ya no puede pasar (no hay texto que matchee)
	got, _ = filter(&spec{Operation: "sale", Zones: []string{"pinares"}})
	if len(got) != 1 || got[0].URL != "a" {
		t.Fatalf("zona mal: %d resultados", len(got))
	}

	if !strings.Contains(compact(1, listings[0]), "venta") {
		t.Fatal("compact sin operación")
	}

	// mixta: se alquila (rent_annual) pero también se vende → matchea "sale"
	// y el precio que se compara es el de venta, no el alquiler mensual.
	mu.Lock()
	listings = append(listings, &model.Listing{URL: "mix",
		Operation: model.Str(model.OperationRentAnnual),
		Price:     model.Float(2500), SalePrice: model.Float(180000)})
	mu.Unlock()
	got, _ = filter(&spec{Operation: "sale", PriceMax: 200000})
	found := false
	for _, l := range got {
		if l.URL == "mix" {
			found = true
		}
	}
	if !found {
		t.Fatal("la mixta con SalePrice no matcheó venta")
	}
	if !strings.Contains(compact(1, listings[len(listings)-1]), "venta USD 180000") {
		t.Fatal("compact sin precio de venta")
	}
}
