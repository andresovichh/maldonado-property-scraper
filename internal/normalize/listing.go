package normalize

import (
	"strings"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

// FromRaw turns a scraped page into the canonical Listing.
//
// The raw map is carried through untouched: when this function changes, history can
// be reprocessed without hitting anyone's website again.
func FromRaw(r *model.RawListing) *model.Listing {
	text, _ := r.Raw["text"].(string)

	l := &model.Listing{
		AgencyDomain: r.AgencyDomain,
		SourceID:     r.SourceID,
		URL:          r.URL,
		Raw:          r.Raw,
		FirstSeenAt:  r.ScrapedAt,
		LastSeenAt:   r.ScrapedAt,
	}

	if og, ok := r.Raw["og"].(map[string]string); ok {
		l.Title = model.Str(strings.TrimSpace(og["title"]))
		l.Description = model.Str(strings.TrimSpace(og["description"]))
		l.ImageURL = model.Str(strings.TrimSpace(og["image"]))
	}

	if ut, ok := r.Raw["url_type"].(string); ok {
		l.PropertyType = model.Str(PropertyType(ut))
	}

	// Counts and features are read from the whole visible text: these sites put the
	// same facts in the spec block on one template and only in the prose on another.
	l.Bedrooms = Bedrooms(text)
	l.Bathrooms = Bathrooms(text)
	l.Suites = Suites(text)
	l.Toilettes = Toilettes(text)

	l.ServiceRoom = ServiceRoom(text)
	l.ServiceBath = ServiceBath(text)
	l.RadiantHeating = RadiantHeating(text)
	l.Heating = Heating(text)
	l.Garage = Garage(text)
	l.Pool = Pool(text)
	l.BBQ = BBQ(text)

	l.BuiltM2 = BuiltM2(text)
	l.LandM2 = LandM2(text)

	l.PeriodPrices = periodPrices(r.Raw)

	// Operation and price come from the price table, not from the URL. A listing
	// sitting under /casas/en-alquiler/ is often temporada only; calling that an
	// annual rental is the single most misleading thing this scraper could do.
	if p, ok := SalePriceOf(l.PeriodPrices); ok {
		l.SalePrice = model.Float(p.Amount)
	} else if a, ok := SaleFromText(text); ok {
		l.SalePrice = model.Float(a)
	}
	if p, ok := AnnualPrice(l.PeriodPrices); ok {
		l.Operation = model.Str(model.OperationRentAnnual)
		l.Currency = model.Str(p.Currency)
		l.Price = model.Float(p.Amount)
	} else if hasRentRow(l.PeriodPrices) {
		l.Operation = model.Str(model.OperationRentSeason)
	} else if l.SalePrice != nil {
		// Venta pura: sin filas de alquiler y con precio de venta.
		l.Operation = model.Str(model.OperationSale)
		l.Currency = model.Str("USD")
		l.Price = l.SalePrice
	}

	return l
}

func hasRentRow(prices []model.PeriodPrice) bool {
	for _, p := range prices {
		if PeriodOperation(p.Period) != model.OperationSale {
			return true
		}
	}
	return false
}

// periodPrices reads the price rows back out of the raw map, tolerating both the
// in-process typed form and the JSON round-tripped one.
func periodPrices(raw map[string]any) []model.PeriodPrice {
	switch v := raw["period_prices"].(type) {
	case []model.PeriodPrice:
		return v
	case []any:
		out := make([]model.PeriodPrice, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			p := model.PeriodPrice{}
			p.Period, _ = m["period"].(string)
			p.Currency, _ = m["currency"].(string)
			if f, ok := m["amount"].(float64); ok {
				p.Amount = f
			}
			out = append(out, p)
		}
		return out
	default:
		return nil
	}
}
