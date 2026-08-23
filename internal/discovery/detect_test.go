package discovery

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// nominaFixture is a member card trimmed from the real CIPEM roster, keeping the
// markup that matters: entry title, tel:/mailto: links, a bare <li> address, and
// the agency's own site.
const nominaFixture = `
<div class="vcex-post-type-entry">
  <h2 class="vcex-post-type-entry-title entry-title">AISPURÚ BIENES RAÍCES</h2>
  <ul>
    <li><a href="tel:+59844862433">(+598) 4486 2433</a></li>
    <li><a href="mailto:juan@aispuru.com">juan@aispuru.com</a></li>
    <li>Faro José Ignacio – Golondrinas casi Cisnes</li>
    <li><a href="http://www.aispuru.com/" target="_blank">aispuru.com</a></li>
  </ul>
</div>
<div class="vcex-post-type-entry">
  <h2 class="entry-title">SOLO FACEBOOK S.A.</h2>
  <ul>
    <li><a href="https://www.facebook.com/solofb">Facebook</a></li>
  </ul>
</div>`

func TestParseNomina(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(nominaFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := parseNomina(doc)
	if len(got) != 2 {
		t.Fatalf("want 2 agencies, got %d: %+v", len(got), got)
	}

	a := got[0]
	if a.Name != "AISPURÚ BIENES RAÍCES" {
		t.Errorf("name = %q", a.Name)
	}
	if a.Domain != "aispuru.com" {
		t.Errorf("domain = %q, want aispuru.com (www stripped)", a.Domain)
	}
	if a.Email != "juan@aispuru.com" {
		t.Errorf("email = %q", a.Email)
	}
	if !strings.Contains(a.Address, "Faro José Ignacio") {
		t.Errorf("address = %q", a.Address)
	}

	// A Facebook link is not an inventory site: the agency must survive the parse
	// (so the totals stay honest) but with no domain to crawl.
	if got[1].Domain != "" {
		t.Errorf("social-only agency got domain %q, want empty", got[1].Domain)
	}
}

func TestClassifyTera(t *testing.T) {
	// Condensed from nicolasdemodena.com: the chatbot snippet plus the footer.
	body := `<html><body>
	  <script>var inmToken='UTDAdp2UIq62';
	    var teraBot = document.createElement('script');
	    teraBot.src = 'https://tera.uy/bot/res/iabotjs?inm=UTDAdp2UIq62';</script>
	  <img src="https://ri.com.uy/f/80/1/400/1/0/0/abc.jpg">
	  <p>Con tecnología de <a href="https://www.tera.com.uy">TERA CRM</a>.
	     Desarrollado por <a href="http://www.sierra.com.uy/">Sierra Soluciones</a></p>
	</body></html>`

	var r Result
	r.classify(body, "")

	if r.Engine != EngineTera {
		t.Errorf("engine = %q, want tera", r.Engine)
	}
	if r.TeraToken != "UTDAdp2UIq62" {
		t.Errorf("tera token = %q", r.TeraToken)
	}
	// Sierra is the web studio, TERA is the CRM. Collapsing them would make us
	// think two different things are one, which is exactly the mistake to avoid.
	if r.Developer != "sierra" {
		t.Errorf("developer = %q, want sierra", r.Developer)
	}
}

func TestClassifyWordPressIsNotTera(t *testing.T) {
	var r Result
	r.classify(`<link href="/wp-content/themes/x/style.css">`, "")
	if r.Engine != EngineWordPress {
		t.Errorf("engine = %q, want wordpress", r.Engine)
	}
	if r.TeraToken != "" {
		t.Errorf("unexpected tera token %q", r.TeraToken)
	}
}

func TestListingHitsNeedsBothSignals(t *testing.T) {
	// Real listing card markup: price AND room counts.
	listing := `<h4 class="list-pr"> U$S 50,000</h4><li>3 Dormitorios</li><li>4 Baños</li>
	            <h4 class="list-pr">USD 2.700</h4><li>3 Dormitorios</li><li>3 Baños</li>
	            <h4 class="list-pr">USD 3.100</h4><li>4 Dormitorios</li><li>2 Baños</li>`
	if got := listingHits(listing); got < 3 {
		t.Errorf("listingHits(listing) = %d, want >= 3", got)
	}

	// A page that only talks about money is not inventory — this is what keeps
	// "sold 200 properties, USD 5.000.000 in sales" off the inventory list.
	prose := `<p>Vendimos por USD 5.000.000 en 2026. Consultá precios: USD 1.200, USD 3.400.</p>`
	if got := listingHits(prose); got != 0 {
		t.Errorf("listingHits(prose) = %d, want 0", got)
	}
}

func TestLooksLikeSPA(t *testing.T) {
	spa := `<html><head><script src="/a.js"></script><script src="/b.js"></script>
	        <script src="/c.js"></script></head><body><div id="root"></div></body></html>`
	if !looksLikeSPA(spa) {
		t.Error("empty shell with 3 scripts should look like a SPA")
	}

	server := `<html><body><script src="/a.js"></script><script src="/b.js"></script>
	           <script src="/c.js"></script>` + strings.Repeat("Casa en alquiler en La Barra. ", 100) +
		`</body></html>`
	if looksLikeSPA(server) {
		t.Error("page with real server-rendered text should not look like a SPA")
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Aispuru.com/x": "aispuru.com",
		"http://gary.uy":            "gary.uy",
		// Real entry in the roster: the FQDN root dot must not create a second domain.
		"http://www.tambolini.com./": "tambolini.com",
		"not a url":                  "",
	} {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFlipWWW(t *testing.T) {
	if got := flipWWW("https://www.x.com/a"); got != "https://x.com/a" {
		t.Errorf("got %q", got)
	}
	if got := flipWWW("https://x.com/a"); got != "https://www.x.com/a" {
		t.Errorf("got %q", got)
	}
}

func TestRiComUyAloneIsNotTera(t *testing.T) {
	// gary.uy serves photos from ri.com.uy but does not answer the family's
	// /{tipo}/en-{operacion}/ URL scheme. Treating the shared image CDN as proof
	// of TERA would have put it in the wrong adapter.
	var r Result
	r.classify(`<img src="https://ri.com.uy/f/80/1/400/abc.jpg">`, "")

	if r.Engine == EngineTera {
		t.Error("ri.com.uy alone must not classify as tera")
	}
	if !r.teraWeak {
		t.Error("ri.com.uy should still raise the weak flag so the URL scheme gets probed")
	}
	if r.teraStrong {
		t.Error("ri.com.uy is not a strong signal")
	}
}

func TestNavLinkHarvestPrefersAnchorText(t *testing.T) {
	// The case that motivated this: a nav item reading "Propiedades" pointing at a
	// path no guessed list would ever contain. Probing a fixed path list marked 51
	// reachable agencies as having no inventory at all.
	home := `<nav>
	  <a href="/es/p/12">Propiedades</a>
	  <a href="/quienes-somos">Nosotros</a>
	  <a href="/blog/venta-2026">Vender en 2026</a>
	  <a href="https://facebook.com/x">Propiedades en Facebook</a>
	  <a href="/fotos/casa.jpg">Casas</a>
	</nav>`

	var got []string
	for _, m := range reNavLink.FindAllStringSubmatch(home, -1) {
		href, text := m[1], stripTags(m[2])
		abs := absURL("https://x.com.uy/", href)
		if HostOf(abs) != "x.com.uy" || reIndexSkip.MatchString(abs) || reIndexSkip.MatchString(text) {
			continue
		}
		if reIndexWord.MatchString(text) || reIndexWord.MatchString(abs) {
			got = append(got, abs)
		}
	}

	if len(got) != 1 || got[0] != "https://x.com.uy/es/p/12" {
		t.Errorf("got %v, want just the Propiedades link", got)
	}
}

func TestListingHitsAcceptsAPriceGrid(t *testing.T) {
	// babencopropiedades lists 22 prices on its rental index and never prints a
	// bedroom count on the card. Demanding both signals scored a real inventory
	// page at zero and lost the agency.
	grid := strings.Repeat(`<div class="prop">USD 2.500</div>`, 12)
	if got := listingHits(grid); got < 10 {
		t.Errorf("listingHits(price grid) = %d, want >= 10", got)
	}
	// Prose about money still must not count.
	prose := `<p>Vendimos por USD 5.000.000 en 2026, con operaciones de USD 1.200 a USD 3.400.</p>`
	if got := listingHits(prose); got != 0 {
		t.Errorf("listingHits(prose) = %d, want 0", got)
	}
}

func TestSameBrandAcrossTLDs(t *testing.T) {
	if !sameBrand("babencopropiedades.com.ar", "babencopropiedades.com.uy") {
		t.Error("same agency on another TLD should count")
	}
	if sameBrand("facebook.com", "gary.uy") {
		t.Error("unrelated hosts must not count")
	}
	// Short labels are too collision-prone to trust.
	if sameBrand("casa.com.ar", "casa.com.uy") {
		t.Error("short shared labels must not count")
	}
}
