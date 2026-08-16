package scraper

import (
	"os"
	"testing"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
	"github.com/andresovichh/maldonado-property-scraper/internal/normalize"
)

func TestExtractDetailURLs(t *testing.T) {
	// Two different card templates from the family, in one page. The adapter must
	// not care which one it is looking at — that is the whole design.
	body := `
	  <div class="single_property_style"><a href="https://www.nicolasdemodena.com/Casa/1271">x</a></div>
	  <div class="property-block-two"><a href="/Apartamento/44912">y</a></div>
	  <a href="https://www.nicolasdemodena.com/Casa/1271">duplicate</a>
	  <a href="/nosotros">not a listing</a>
	  <a href="/casas/en-alquiler/">also not a listing</a>`

	got := ExtractDetailURLs(body, "https://www.nicolasdemodena.com/casas/en-alquiler/")
	want := []string{
		"https://www.nicolasdemodena.com/Casa/1271",
		"https://www.nicolasdemodena.com/Apartamento/44912",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d urls %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListingIndexURL(t *testing.T) {
	got := ListingIndexURL("https://www.javiersena.com", "casas", "alquiler", PeriodoAnual)
	want := "https://www.javiersena.com/casas/en-alquiler/?periodo=17"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ListingIndexURL("https://x.com/", "casas", "venta", 0); got != "https://x.com/casas/en-venta/" {
		t.Errorf("got %q", got)
	}
}

func TestParsePriceTableSeparatesAnnualFromSeason(t *testing.T) {
	// Verbatim shape from a real page. The January figure is a summer fortnight,
	// not a monthly rent — mixing them up is the worst thing this code could do.
	text := `Precios de Alquiler 2da Quincena Enero U$S 17.000 1er Quincena Febrero U$S 12.000 ` +
		`2da Quincena Febrero U$S 12.000 Anual en Dólares U$S 7.500`

	prices := ParsePriceTable(text)
	if len(prices) != 4 {
		t.Fatalf("got %d rows: %+v", len(prices), prices)
	}

	annual, ok := normalize.AnnualPrice(prices)
	if !ok {
		t.Fatal("no annual price found")
	}
	if annual.Amount != 7500 {
		t.Errorf("annual = %v, want 7500", annual.Amount)
	}
	if annual.Currency != "USD" {
		t.Errorf("currency = %q, want USD", annual.Currency)
	}

	for _, p := range prices {
		if p.Period != annual.Period && normalize.PeriodOperation(p.Period) != model.OperationRentSeason {
			t.Errorf("%q classified as %q, want season", p.Period, normalize.PeriodOperation(p.Period))
		}
	}
}

func TestParseDetailOnRealPage(t *testing.T) {
	body, err := os.ReadFile("testdata/detail_annual.html")
	if err != nil {
		t.Fatal(err)
	}

	raw := ParseDetail("adrianamartino.com", "https://www.adrianamartino.com/Casa/32672", string(body))
	if raw.SourceID != "32672" {
		t.Errorf("source id = %q, want 32672", raw.SourceID)
	}

	l := normalize.FromRaw(raw)

	if l.PropertyType == nil || *l.PropertyType != model.TypeHouse {
		t.Errorf("property type = %v, want house", l.PropertyType)
	}
	if l.Operation == nil || *l.Operation != model.OperationRentAnnual {
		t.Errorf("operation = %v, want rent_annual", l.Operation)
	}
	if l.Price == nil || *l.Price != 7500 {
		t.Errorf("price = %v, want 7500", l.Price)
	}
	// The ad says "cuatro dormitorios, cuatro baños y dependencia de servicio":
	// spelled-out numbers are not parsed, so the counts may be nil — but the
	// service room must be found, because that is a scoring feature.
	if l.ServiceRoom == nil || !*l.ServiceRoom {
		t.Errorf("service room = %v, want true", l.ServiceRoom)
	}
	if l.Title == nil || *l.Title == "" {
		t.Error("title should come from og:title")
	}
	// Raw text must survive so the normaliser can be rerun over history.
	if txt, ok := raw.Raw["text"].(string); !ok || len(txt) < 200 {
		t.Error("raw text not preserved")
	}
}

func TestVisibleTextDropsScriptsAndEntities(t *testing.T) {
	got := VisibleText(`<div>Casa&nbsp;en La Barra<script>var x='oculto';</script><b>3</b> dorm</div>`)
	want := "Casa en La Barra 3 dorm"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
