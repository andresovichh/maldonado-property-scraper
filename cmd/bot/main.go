// Command bot is the personal Telegram bot over the crawled listings.
//
// Flow per message: Haiku #1 turns the natural-language query into a filter
// spec, Go narrows ~4.400 listings to ≤150 candidates, Haiku #2 reads them and
// answers with links. No vector store: at this corpus size the candidates fit
// straight into the model's context.
//
// Env: TELEGRAM_TOKEN, ANTHROPIC_API_KEY, ALLOWED_CHAT (one chat id).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andresovichh/maldonado-property-scraper/internal/model"
)

const (
	haikuModel   = "claude-haiku-4-5-20251001"
	candidateCap = 150
	crawlHour    = 7 // daily refresh, local time
)

var (
	tgToken     = mustEnv("TELEGRAM_TOKEN")
	apiKey      = mustEnv("ANTHROPIC_API_KEY")
	allowedChat = envOr("ALLOWED_CHAT", "610787415")

	httpc = &http.Client{Timeout: 120 * time.Second}

	mu       sync.RWMutex
	listings []*model.Listing

	crawlMu sync.Mutex // one crawl at a time
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("falta %s", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if err := reload(); err != nil {
		log.Fatalf("cargando listings: %v", err)
	}
	log.Printf("bot arriba: %d listings, chat autorizado %s", count(), allowedChat)

	go dailyCrawl()

	var offset int64
	for {
		updates, err := getUpdates(offset)
		if err != nil {
			log.Printf("getUpdates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			chat := fmt.Sprint(u.Message.Chat.ID)
			if chat != allowedChat {
				log.Printf("ignorado chat no autorizado %s (%q)", chat, u.Message.Text)
				continue
			}
			go handle(chat, strings.TrimSpace(u.Message.Text))
		}
	}
}

// ─── Telegram ───────────────────────────────────────────────────────────

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func getUpdates(offset int64) ([]tgUpdate, error) {
	resp, err := httpc.Get(fmt.Sprintf(
		"https://api.telegram.org/bot%s/getUpdates?timeout=50&offset=%d", tgToken, offset))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram !ok")
	}
	return out.Result, nil
}

func send(chat, text string) {
	// Telegram caps messages at 4096 chars; split on paragraph edges.
	for len(text) > 0 {
		chunk := text
		if len(chunk) > 4000 {
			cut := strings.LastIndex(chunk[:4000], "\n")
			if cut < 1000 {
				cut = 4000
			}
			chunk, text = text[:cut], text[cut:]
		} else {
			text = ""
		}
		_, err := httpc.PostForm("https://api.telegram.org/bot"+tgToken+"/sendMessage", url.Values{
			"chat_id":                  {chat},
			"text":                     {chunk},
			"disable_web_page_preview": {"true"},
		})
		if err != nil {
			log.Printf("send: %v", err)
		}
	}
}

func typing(chat string) {
	httpc.PostForm("https://api.telegram.org/bot"+tgToken+"/sendChatAction",
		url.Values{"chat_id": {chat}, "action": {"typing"}})
}

// ─── Comandos ───────────────────────────────────────────────────────────

func handle(chat, text string) {
	switch {
	case text == "/start" || text == "/help":
		send(chat, "Buscador de propiedades de Maldonado.\n\n"+
			"Escribime la búsqueda en lenguaje natural, por ejemplo:\n"+
			"«casa en alquiler anual, 3 dormitorios, hasta 3000 dólares, con piscina»\n"+
			"«apto en venta en Punta del Este hasta 200 mil»\n\n"+
			"/actualizar — re-crawlear las inmobiliarias ahora\n"+
			"/stats — qué hay cargado")
	case text == "/stats":
		send(chat, stats())
	case text == "/actualizar":
		go runCrawl(chat)
	default:
		answer(chat, text)
	}
}

func stats() string {
	mu.RLock()
	defer mu.RUnlock()
	byOp := map[string]int{}
	agencies := map[string]bool{}
	for _, l := range listings {
		op := "sin_operacion"
		if l.Operation != nil {
			op = *l.Operation
		}
		byOp[op]++
		agencies[l.AgencyDomain] = true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d propiedades de %d inmobiliarias\n", len(listings), len(agencies))
	ops := make([]string, 0, len(byOp))
	for op := range byOp {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		fmt.Fprintf(&sb, "  %s: %d\n", opLabel(op), byOp[op])
	}
	return sb.String()
}

// ─── Consulta: Haiku #1 (parseo) → filtro Go → Haiku #2 (respuesta) ─────

type spec struct {
	Operation    string   `json:"operation"`
	PropertyType string   `json:"property_type"`
	PriceMin     float64  `json:"price_min"`
	PriceMax     float64  `json:"price_max"`
	Bedrooms     int      `json:"bedrooms"`
	Bathrooms    int      `json:"bathrooms"`
	Zones        []string `json:"zones"`
	Keywords     []string `json:"keywords"`
}

const parseSystem = `Convertís consultas inmobiliarias en español a un JSON de filtros. Respondé ÚNICAMENTE el JSON, sin explicación ni fences.

Esquema (omití o poné null/0/[] lo que la consulta no diga):
{
 "operation": "rent_annual" | "rent_season" | "rent_winter" | "sale" | "rent_any" | "any",
 "property_type": "house" | "apartment" | "chacra" | "field" | "land" | "commercial" | "any",
 "price_min": número (USD),
 "price_max": número (USD; "200 mil" = 200000),
 "bedrooms": número,
 "bathrooms": número,
 "zones": ["barrios o zonas mencionadas, en minúsculas"],
 "keywords": ["otras características pedidas: piscina, garaje, parrillero, losa radiante, vista al mar, ..."]
}

"alquiler" sin más contexto = "rent_any". Si piden alquiler y el precio suena mensual, dejalo tal cual (los precios de la base son mensuales para alquiler anual).`

func answer(chat, query string) {
	typing(chat)
	sp, err := parseQuery(query)
	if err != nil {
		send(chat, "No pude interpretar la consulta: "+err.Error())
		return
	}
	cands, total := filter(sp)
	if len(cands) == 0 {
		send(chat, "Ninguna propiedad pasa esos filtros. Probá aflojando precio o zona (/stats para ver qué hay).")
		return
	}
	typing(chat)
	reply, err := rankAndAnswer(query, sp, cands, total)
	if err != nil {
		send(chat, "Error consultando el modelo: "+err.Error())
		return
	}
	send(chat, reply)
}

func parseQuery(q string) (*spec, error) {
	raw, err := anthropic(parseSystem, q, 400)
	if err != nil {
		return nil, err
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("sin JSON en la respuesta")
	}
	var sp spec
	if err := json.Unmarshal([]byte(raw[start:end+1]), &sp); err != nil {
		return nil, err
	}
	return &sp, nil
}

// filter keeps every listing that does not CONTRADICT the spec: unknown fields
// pass. Ranking real lo hace Haiku #2; esto solo achica el contexto.
func filter(sp *spec) ([]*model.Listing, int) {
	mu.RLock()
	defer mu.RUnlock()

	type scored struct {
		l *model.Listing
		s int
	}
	var out []scored
	for _, l := range listings {
		wantSale := sp.Operation == "sale"
		if wantSale {
			// contradicción conocida: opera como alquiler y no tiene precio de venta
			if l.SalePrice == nil && l.Operation != nil && *l.Operation != model.OperationSale {
				continue
			}
		} else if !opMatches(sp.Operation, l.Operation) {
			continue
		}
		if sp.PropertyType != "" && sp.PropertyType != "any" &&
			l.PropertyType != nil && *l.PropertyType != sp.PropertyType {
			continue
		}
		price := l.Price
		if wantSale && l.SalePrice != nil {
			price = l.SalePrice
		}
		if price != nil {
			if sp.PriceMax > 0 && *price > sp.PriceMax*1.25 {
				continue
			}
			if sp.PriceMin > 0 && *price < sp.PriceMin*0.75 {
				continue
			}
		}
		if sp.Bedrooms > 0 && l.Bedrooms != nil && abs(*l.Bedrooms-sp.Bedrooms) > 1 {
			continue
		}
		blob := strings.ToLower(join(l.City) + " " + join(l.Neighborhood) + " " +
			join(l.Title) + " " + join(l.Description))
		if len(sp.Zones) > 0 && !containsAny(blob, sp.Zones) {
			continue
		}
		s := 0
		if l.Price != nil || l.SalePrice != nil {
			s += 3
		}
		if l.Bedrooms != nil {
			s++
		}
		for _, kw := range sp.Keywords {
			if strings.Contains(blob, strings.ToLower(kw)) {
				s += 2
			}
		}
		out = append(out, scored{l, s})
	}
	total := len(out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].s > out[j].s })
	if len(out) > candidateCap {
		out = out[:candidateCap]
	}
	res := make([]*model.Listing, len(out))
	for i, s := range out {
		res[i] = s.l
	}
	return res, total
}

func opMatches(want string, have *string) bool {
	if want == "" || want == "any" || have == nil {
		return true
	}
	if want == "rent_any" {
		return strings.HasPrefix(*have, "rent_")
	}
	return *have == want
}

func rankAndAnswer(query string, sp *spec, cands []*model.Listing, total int) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "CONSULTA: %s\n\nCANDIDATOS (%d mostrados de %d que pasaron el filtro):\n",
		query, len(cands), total)
	for i, l := range cands {
		sb.WriteString(compact(i+1, l))
	}
	sys := "Sos el asistente inmobiliario personal del operador, sobre una base scrapeada de inmobiliarias de Maldonado, Uruguay. " +
		"Recomendá como máximo 10 propiedades de los CANDIDATOS, las que mejor calcen con la consulta — incluso si se pasan un poco de precio, avisándolo. " +
		"Por cada una: precio, lo esencial, por qué la elegiste y su URL en línea aparte. " +
		"Los precios de alquiler son mensuales en USD salvo que se indique otra cosa. " +
		"Terminá con una línea de resumen. Español rioplatense, directo, sin markdown (Telegram texto plano)."
	return anthropic(sys, sb.String(), 2000)
}

func compact(i int, l *model.Listing) string {
	var p []string
	if l.Operation != nil {
		p = append(p, opLabel(*l.Operation))
	}
	if l.PropertyType != nil {
		p = append(p, *l.PropertyType)
	}
	loc := strings.TrimSpace(join(l.Neighborhood) + " " + join(l.City))
	if loc != "" {
		p = append(p, loc)
	}
	if l.Price != nil {
		cur := "USD"
		if l.Currency != nil {
			cur = *l.Currency
		}
		p = append(p, fmt.Sprintf("%s %.0f", cur, *l.Price))
	}
	if l.SalePrice != nil && (l.Price == nil || l.Price != l.SalePrice) {
		p = append(p, fmt.Sprintf("venta USD %.0f", *l.SalePrice))
	}
	if l.Bedrooms != nil {
		p = append(p, fmt.Sprintf("%dd", *l.Bedrooms))
	}
	if l.Bathrooms != nil {
		p = append(p, fmt.Sprintf("%db", *l.Bathrooms))
	}
	if l.BuiltM2 != nil {
		p = append(p, fmt.Sprintf("%.0fm2", *l.BuiltM2))
	}
	var feats []string
	for name, v := range map[string]*bool{"piscina": l.Pool, "garaje": l.Garage,
		"parrillero": l.BBQ, "losa radiante": l.RadiantHeating,
		"dep.servicio": l.ServiceRoom} {
		if v != nil && *v {
			feats = append(feats, name)
		}
	}
	sort.Strings(feats)
	if len(feats) > 0 {
		p = append(p, strings.Join(feats, ","))
	}
	line := fmt.Sprintf("%d) %s | %s | %s\n", i, strings.Join(p, " | "),
		trunc(join(l.Title), 90), l.URL)
	if d := trunc(join(l.Description), 160); d != "" {
		line += "   " + d + "\n"
	}
	return line
}

func opLabel(op string) string {
	switch op {
	case model.OperationRentAnnual:
		return "alq.anual"
	case model.OperationRentSeason:
		return "temporada"
	case model.OperationRentWinter:
		return "invernal"
	case model.OperationSale:
		return "venta"
	case model.OperationRentUnknown:
		return "alquiler"
	}
	return op
}

// ─── Anthropic ──────────────────────────────────────────────────────────

func anthropic(system, user string, maxTokens int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      haikuModel,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	})
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Content) == 0 {
		return "", fmt.Errorf("respuesta vacía")
	}
	return out.Content[0].Text, nil
}

// ─── Datos y crawl ──────────────────────────────────────────────────────

func reload() error {
	files, _ := filepath.Glob("out/listings*.json")
	if len(files) == 0 {
		return fmt.Errorf("no hay out/listings*.json")
	}
	seen := map[string]bool{}
	var all []*model.Listing
	for _, f := range files {
		if strings.HasSuffix(f, ".html") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var ls []*model.Listing
		if err := json.Unmarshal(b, &ls); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		for _, l := range ls {
			key := l.URL
			if l.Operation != nil {
				key += "|" + *l.Operation
			}
			if !seen[key] {
				seen[key] = true
				all = append(all, l)
			}
		}
	}
	mu.Lock()
	listings = all
	mu.Unlock()
	return nil
}

func count() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(listings)
}

func runCrawl(chat string) {
	if !crawlMu.TryLock() {
		send(chat, "Ya hay un crawl corriendo.")
		return
	}
	defer crawlMu.Unlock()
	send(chat, "Crawleando (alquiler + venta, tarda varios minutos)…")
	before := urlSet()
	for _, args := range [][]string{
		{"run", "./cmd/crawl", "-out", "out/listings.json"},
		{"run", "./cmd/crawl", "-operation", "venta", "-out", "out/listings-venta.json"},
	} {
		cmd := exec.Command("go", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			send(chat, fmt.Sprintf("Crawl falló (%v):\n%s", err, tail(string(out), 600)))
			return
		}
	}
	if err := reload(); err != nil {
		send(chat, "Crawl ok pero falló la recarga: "+err.Error())
		return
	}
	nuevos := 0
	mu.RLock()
	for _, l := range listings {
		if !before[l.URL] {
			nuevos++
		}
	}
	mu.RUnlock()
	send(chat, fmt.Sprintf("Listo: %d propiedades cargadas, %d nuevas desde el último crawl.", count(), nuevos))
}

func urlSet() map[string]bool {
	mu.RLock()
	defer mu.RUnlock()
	s := make(map[string]bool, len(listings))
	for _, l := range listings {
		s[l.URL] = true
	}
	return s
}

func dailyCrawl() {
	last := ""
	for range time.Tick(30 * time.Minute) {
		now := time.Now()
		day := now.Format("2006-01-02")
		if now.Hour() == crawlHour && day != last {
			last = day
			runCrawl(allowedChat)
		}
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────

func join(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func containsAny(blob string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(blob, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
