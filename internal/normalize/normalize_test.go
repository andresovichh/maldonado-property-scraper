package normalize

import (
	"testing"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

func TestParseAmountUruguayanFormat(t *testing.T) {
	// The one that matters: "7.500" is seven thousand five hundred. Reading the dot
	// as a decimal point turns a USD 7.500 annual rent into USD 7.50 and puts the
	// listing at the top of every cheap-first ranking.
	cases := map[string]float64{
		"7.500":     7500,
		"10000":     10000,
		"17.000":    17000,
		"2,100":     2100,
		"2.500,50":  2500.50,
		"2,500.50":  2500.50,
		"1.700":     1700,
		"0":         0,
		"3.100.000": 3100000,
	}
	for in, want := range cases {
		got, ok := ParseAmount(in)
		if !ok {
			t.Errorf("ParseAmount(%q) failed", in)
			continue
		}
		if got != want {
			t.Errorf("ParseAmount(%q) = %v, want %v", in, got, want)
		}
	}
	if _, ok := ParseAmount("consultar"); ok {
		t.Error("ParseAmount should reject non-numeric text")
	}
}

func TestAnnualPriceIgnoresZeroSentinel(t *testing.T) {
	// Observed on real listings: "Anual: U$S 0" means "we don't rent this annually".
	// Taken literally it is the cheapest house in Maldonado.
	prices := []model.PeriodPrice{
		{Period: "Enero", Currency: "USD", Amount: 10000},
		{Period: "Anual", Currency: "USD", Amount: 0},
	}
	if _, ok := AnnualPrice(prices); ok {
		t.Error("Anual = 0 must be treated as absent, not as a free house")
	}

	prices = append(prices, model.PeriodPrice{Period: "Anual en Dólares", Currency: "USD", Amount: 7500})
	got, ok := AnnualPrice(prices)
	if !ok || got.Amount != 7500 {
		t.Errorf("AnnualPrice = %+v, %v; want the 7500 row", got, ok)
	}
}

func TestPeriodOperation(t *testing.T) {
	cases := map[string]string{
		"Venta":              model.OperationSale,
		"Venta (Dólares)":    model.OperationSale,
		"Anual":              model.OperationRentAnnual,
		"Anual en Dólares":   model.OperationRentAnnual,
		"Anual (Dólares)":    model.OperationRentAnnual,
		"Invernal":           model.OperationRentWinter,
		"Enero":              model.OperationRentSeason,
		"1ª Quin. Enero":     model.OperationRentSeason,
		"2da Quincena Enero": model.OperationRentSeason,
		"Carnaval":           model.OperationRentSeason,
	}
	for in, want := range cases {
		if got := PeriodOperation(in); got != want {
			t.Errorf("PeriodOperation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRadiantHeatingCatchesMisspelling(t *testing.T) {
	// "loza radiante" (with z) is how it is written in a large share of real ads.
	// Missing it drops exactly the feature the buyer cares most about.
	for _, s := range []string{
		"cuenta con losa radiante en toda la casa",
		"calefacción por loza radiante sectorizada",
		"piso radiante en baños",
		"calefacción radiante",
	} {
		got := RadiantHeating(s)
		if got == nil || !*got {
			t.Errorf("RadiantHeating(%q) = %v, want true", s, got)
		}
	}
	if got := RadiantHeating("aire acondicionado y estufa a leña"); got != nil {
		t.Errorf("RadiantHeating(no radiant) = %v, want nil (not stated)", got)
	}
}

func TestNegationFlipsFeature(t *testing.T) {
	got := Pool("hermosa casa sin piscina, con parrillero")
	if got == nil || *got {
		t.Errorf("Pool(sin piscina) = %v, want false", got)
	}
	// And the same text should still find the barbecue.
	if b := BBQ("hermosa casa sin piscina, con parrillero"); b == nil || !*b {
		t.Errorf("BBQ = %v, want true", b)
	}
}

func TestUnstatedFeatureIsNil(t *testing.T) {
	// nil ≠ false. A listing that never mentions a service room must not be ranked
	// as one that says it has none.
	if got := ServiceRoom("casa de 3 dormitorios en La Barra"); got != nil {
		t.Errorf("ServiceRoom(unstated) = %v, want nil", got)
	}
}

func TestServiceRoomVariants(t *testing.T) {
	for _, s := range []string{
		"dependencia de servicio",
		"cuarto de servicio con baño",
		"dormitorio de servicio",
		"3 dorm + dependencia",
	} {
		if got := ServiceRoom(s); got == nil || !*got {
			t.Errorf("ServiceRoom(%q) = %v, want true", s, got)
		}
	}
}

func TestRoomCountsBothOrders(t *testing.T) {
	// Label-first, as printed by one part of the family…
	if got := Bedrooms("Casa # 2054 En Alquiler Dorms.: 3 Baños: 2"); got == nil || *got != 3 {
		t.Errorf("Bedrooms(label-first) = %v, want 3", got)
	}
	if got := Bathrooms("Casa # 2054 En Alquiler Dorms.: 3 Baños: 2"); got == nil || *got != 2 {
		t.Errorf("Bathrooms(label-first) = %v, want 2", got)
	}
	// …number-first, as printed by another.
	if got := Bedrooms("Cuenta con 2 Dormitorios 1 Baños"); got == nil || *got != 2 {
		t.Errorf("Bedrooms(number-first) = %v, want 2", got)
	}
	if got := Bathrooms("Cuenta con 2 Dormitorios 1 Baños"); got == nil || *got != 1 {
		t.Errorf("Bathrooms(number-first) = %v, want 1", got)
	}
	if got := Bedrooms("casa linda en La Barra"); got != nil {
		t.Errorf("Bedrooms(unstated) = %v, want nil", got)
	}
}

func TestToiletteIsNotABathroom(t *testing.T) {
	// "2 y toilette" is 2 bathrooms + 1 half-bath, not 3 bathrooms.
	const s = "Baños: 2 y toilette"
	if got := Bathrooms(s); got == nil || *got != 2 {
		t.Errorf("Bathrooms(%q) = %v, want 2", s, got)
	}
	if got := Toilettes(s); got == nil || *got != 1 {
		t.Errorf("Toilettes(%q) = %v, want 1", s, got)
	}
}

func TestZeroSurfaceIsUnknown(t *testing.T) {
	// The family prints "Superficie 0 m2" when nothing was loaded.
	if got := LandM2("Sup. Total 0 m 2"); got != nil {
		t.Errorf("LandM2(0) = %v, want nil", got)
	}
	if got := LandM2("Sup. Terreno 650m 2"); got == nil || *got != 650 {
		t.Errorf("LandM2(650) = %v, want 650", got)
	}
}

func TestCurrency(t *testing.T) {
	for in, want := range map[string]string{
		"U$S": "USD", "USD": "USD", "US$": "USD", "$": "UYU", "": "",
	} {
		if got := Currency(in); got != want {
			t.Errorf("Currency(%q) = %q, want %q", in, got, want)
		}
	}
}
