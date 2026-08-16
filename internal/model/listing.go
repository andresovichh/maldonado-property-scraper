// Package model holds the shapes that travel between scraping, normalisation and
// storage.
package model

import "time"

// Operation is what the listing is offered for.
const (
	OperationRentAnnual  = "rent_annual"  // alquiler anual — what we are shopping for
	OperationRentSeason  = "rent_season"  // temporada / quincenas
	OperationRentWinter  = "rent_winter"  // invernal
	OperationSale        = "sale"         // venta
	OperationRentUnknown = "rent_unknown" // offered for rent, period not stated
)

// Property types, normalised.
const (
	TypeHouse     = "house"
	TypeApartment = "apartment"
	TypeChacra    = "chacra"
	TypeField     = "field"
	TypeLand      = "land"
	TypeCommercial = "commercial"
	TypeOther     = "other"
)

// RawListing is exactly what a scraper saw, before anyone interprets it. It is
// stored verbatim so a change in the normaliser can be replayed over history
// without re-scraping — which is the whole reason the raw layer exists.
type RawListing struct {
	AgencyDomain string         `json:"agency_domain"`
	SourceID     string         `json:"source_id"`
	URL          string         `json:"url"`
	Raw          map[string]any `json:"raw"`
	ScrapedAt    time.Time      `json:"scraped_at"`
}

// PeriodPrice is one row of the "Precios de Alquiler" table: a rental period and
// what it costs.
type PeriodPrice struct {
	Period   string  `json:"period"`   // as printed: "Enero", "1ª Quin. Enero", "Anual en Dólares"
	Currency string  `json:"currency"` // "USD" | "UYU"
	Amount   float64 `json:"amount"`
}

// Listing is the normalised view. Every optional field is a pointer so the three
// states stay distinct:
//
//	true/value → we know
//	false/0    → we know it is absent/zero
//	nil        → the listing does not say
//
// Collapsing "not stated" into "false" would quietly turn every silent listing into
// a house with no service room, which is exactly the ranking bug to avoid.
type Listing struct {
	AgencyDomain string `json:"agency_domain"`
	SourceID     string `json:"source_id"`
	URL          string `json:"url"`

	Operation    *string `json:"operation,omitempty"`
	PropertyType *string `json:"property_type,omitempty"`

	Department   *string `json:"department,omitempty"`
	City         *string `json:"city,omitempty"`
	Neighborhood *string `json:"neighborhood,omitempty"`

	Currency *string `json:"currency,omitempty"`
	// Price is the annual rent when we could find one. Seasonal prices live in
	// PeriodPrices and must never be mistaken for it: a January price of USD 10.000
	// is not a monthly rent.
	Price *float64 `json:"price,omitempty"`

	PeriodPrices []PeriodPrice `json:"period_prices,omitempty"`

	Bedrooms  *int `json:"bedrooms,omitempty"`
	Bathrooms *int `json:"bathrooms,omitempty"`
	Suites    *int `json:"suites,omitempty"`
	Toilettes *int `json:"toilettes,omitempty"`

	ServiceRoom *bool `json:"service_room,omitempty"`
	ServiceBath *bool `json:"service_bath,omitempty"`

	RadiantHeating *bool `json:"radiant_heating,omitempty"`
	Heating        *bool `json:"heating,omitempty"`

	Garage *bool `json:"garage,omitempty"`
	Pool   *bool `json:"pool,omitempty"`
	BBQ    *bool `json:"bbq,omitempty"`

	LandM2  *float64 `json:"land_m2,omitempty"`
	BuiltM2 *float64 `json:"built_m2,omitempty"`

	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	ImageURL    *string `json:"image_url,omitempty"`

	Raw map[string]any `json:"raw,omitempty"`

	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Helpers for building pointer fields without a local variable at every call site.

func Str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func Int(i int) *int { return &i }

func Float(f float64) *float64 { return &f }

func Bool(b bool) *bool { return &b }
