package scraper

import (
	"os"
	"strings"
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

func TestPageURLsHandlesSingleQuotedPager(t *testing.T) {
	// Verbatim shape from bintang.com.uy. The pager uses SINGLE quotes while the
	// rest of the page uses double — a double-quote-only scan concluded there was
	// no pagination at all and silently capped that agency at 18 of 68 listings.
	body := `<ul class='pagination'>` +
		`<li class='page-item active'><a href='https://www.bintang.com.uy/casas/en-alquiler/?periodo=99&pagina=1' class='page-link'>1</a></li>` +
		`<li class='page-item'><a href='https://www.bintang.com.uy/casas/en-alquiler/?periodo=99&pagina=2' class='page-link'>2</a></li>` +
		`<li class='page-item'><a href='https://www.bintang.com.uy/casas/en-alquiler/?periodo=99&pagina=3' class='page-link'>3</a></li>` +
		`</ul>`

	got := PageURLs(body, "https://www.bintang.com.uy/casas/en-alquiler/")
	// Page 1 is the index we already have; only 2 and 3 are new work.
	if len(got) != 2 {
		t.Fatalf("got %d pages %v, want 2", len(got), got)
	}
	if !strings.Contains(got[0], "pagina=2") || !strings.Contains(got[1], "pagina=3") {
		t.Errorf("pages out of order or wrong: %v", got)
	}
}

func TestResultCount(t *testing.T) {
	if n, ok := ResultCount(`<span>68 Resultados Encontrados </span>`); !ok || n != 68 {
		t.Errorf("got %d, %v; want 68", n, ok)
	}
	if _, ok := ResultCount(`<span>Casas en Alquiler</span>`); ok {
		t.Error("should not invent a count")
	}
}

func TestCandidateURLsPrefersLikelyDetailPages(t *testing.T) {
	body := `
	  <a href="/propiedad/1234">casa</a>
	  <a href="/inmuebles/casa-en-la-barra-887">otra</a>
	  <a href="/contacto">contacto</a>
	  <a href="/nosotros">nosotros</a>
	  <a href="https://facebook.com/x/propiedad/9">otro sitio</a>
	  <a href="/imagenes/casa-1234.jpg">foto</a>
	  <a href="/blog/mercado-inmobiliario-2026">nota</a>`

	got := CandidateURLs(body, "https://ejemplo.com.uy/propiedades", 0)
	if len(got) != 2 {
		t.Fatalf("got %d candidates %v, want 2", len(got), got)
	}
	for _, u := range got {
		if !strings.HasPrefix(u, "https://ejemplo.com.uy/") {
			t.Errorf("%q escaped the site", u)
		}
	}
}

func TestLooksLikeListingNeedsPriceAndRooms(t *testing.T) {
	full := &model.Listing{
		Bedrooms:     model.Int(3),
		PeriodPrices: []model.PeriodPrice{{Period: "Anual", Currency: "USD", Amount: 2500}},
	}
	if !LooksLikeListing(full) {
		t.Error("a page with rooms and a price is a listing")
	}
	// A category page mentions plenty of rooms but carries no price of its own.
	if LooksLikeListing(&model.Listing{Bedrooms: model.Int(3)}) {
		t.Error("rooms alone must not pass — that is how category pages sneak in")
	}
	if LooksLikeListing(&model.Listing{Price: model.Float(2500)}) {
		t.Error("a price alone must not pass")
	}
}
