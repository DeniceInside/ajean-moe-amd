package ajean

// web_search_engines.go — web_search sans clé : les pages de résultats de
// moteurs publics, l'un après l'autre quand l'un échoue.
//
// Mesuré depuis une IP de datacenter française (2026-10) :
// DuckDuckGo bloque 8 requêtes en 6 s pendant environ 2 minutes (HTTP 202 et un
// captcha) ; Bing ne bloque jamais mais, après environ 25 requêtes en 10
// minutes, ne répond plus que sur le premier mot (10 résultats qui ont l'air
// bons : « rust tokio select example » donnait le jeu Rust) ; Brave donne les
// meilleurs extraits et répond 429 après environ 7 requêtes en 5 minutes ;
// Yahoo coupe les connexions en rafale ; Wikipédia répond toujours, sur ce qui
// est encyclopédique. Chaque moteur garde son propre rythme pour tout le process.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	searchEnough     = 3 // moins de résultats que ça : on complète avec le moteur suivant
	searchTimeout    = 10 * time.Second
	searchSnippetMax = 400 // les réponses en ligne de Brave sont longues
)

// Les pages des moteurs (variables pour les tests) ; wikiSearchURL prend la langue.
var (
	bingSearchURL  = "https://www.bing.com/search"
	braveSearchURL = "https://search.brave.com/search"
	yahooSearchURL = "https://search.yahoo.com/search"
	wikiSearchURL  = "https://%s.wikipedia.org/w/rest.php/v1/search/page"
)

// searchBlocked : le moteur a refusé la requête (contrôle anti-robot, limite de
// débit, résultats qui ignorent la requête) ; il est mis de côté un moment.
type searchBlocked struct{ why string }

func (b *searchBlocked) Error() string { return b.why }

var errSearchResting = errors.New("mis de côté")

// searchEngine : un moteur et son rythme, partagés par toutes les requêtes.
type searchEngine struct {
	name   string
	gap    time.Duration // au moins ce délai entre deux requêtes
	cool   time.Duration // durée de mise de côté après un refus
	search func(ctx context.Context, q string, limit int) ([]searchResult, error)

	mu          sync.Mutex
	last, until time.Time
}

var (
	ddgEngine   = &searchEngine{name: "DuckDuckGo", gap: 3 * time.Second, cool: 3 * time.Minute, search: ddgEngineSearch}
	bingEngine  = &searchEngine{name: "Bing", gap: time.Second, cool: 30 * time.Minute, search: bingEngineSearch}
	braveEngine = &searchEngine{name: "Brave", gap: 2 * time.Second, cool: 15 * time.Minute, search: braveEngineSearch}
	yahooEngine = &searchEngine{name: "Yahoo", gap: time.Second, cool: 5 * time.Minute, search: yahooEngineSearch}
	wikiEngine  = &searchEngine{name: "Wikipédia", cool: time.Minute, search: wikiEngineSearch}
)

// searchChain : les moteurs dans l'ordre où on les interroge (variable pour les tests).
var searchChain = func() []*searchEngine {
	return []*searchEngine{ddgEngine, bingEngine, braveEngine, yahooEngine, wikiEngine}
}

// turn attend le prochain créneau libre du moteur : les requêtes passent chacune
// leur tour (borné par ctx). errSearchResting si le moteur est mis de côté.
func (e *searchEngine) turn(ctx context.Context) error {
	e.mu.Lock()
	now := time.Now()
	if now.Before(e.until) {
		e.mu.Unlock()
		return errSearchResting
	}
	at, before := now, e.last
	if next := e.last.Add(e.gap); next.After(at) {
		at = next
	}
	e.last = at
	e.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			e.mu.Lock()
			if e.last.Equal(at) { // le créneau est rendu, sauf si une requête a pris le suivant
				e.last = before
			}
			e.mu.Unlock()
			return ctx.Err()
		case <-t.C:
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Now().Before(e.until) { // une requête passée avant celle-ci a été refusée
		return errSearchResting
	}
	return nil
}

func (e *searchEngine) rest(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if u := time.Now().Add(d); u.After(e.until) {
		e.until = u
	}
}

// query interroge le moteur à son tour ; un refus le met de côté pour sa durée,
// une panne (pas de réponse à temps, erreur serveur) pour une minute.
func (e *searchEngine) query(ctx context.Context, q string, limit int) ([]searchResult, error) {
	if err := e.turn(ctx); err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	rs, err := e.search(qctx, q, limit)
	var b *searchBlocked
	switch {
	case err == nil:
		return rs, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.As(err, &b):
		e.rest(e.cool)
	default:
		e.rest(time.Minute)
	}
	return nil, err
}

// multiSearch cherche q en passant d'un moteur au suivant tant qu'il y a moins
// de searchEnough résultats. from : les moteurs qui ont apporté des résultats.
func multiSearch(q string, limit int) (results []searchResult, from []string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var why []string
	answered := false
	seen := map[string]bool{}
	for _, e := range searchChain() {
		if len(results) >= searchEnough {
			break
		}
		rs, qerr := e.query(ctx, q, limit)
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if qerr != nil {
			why = append(why, e.name+" "+searchReason(qerr))
			continue
		}
		answered = true
		added := 0
		for _, r := range rs {
			if k := searchURLKey(r.URL); r.URL != "" && !seen[k] {
				seen[k] = true
				results = append(results, r)
				added++
			}
		}
		if added > 0 {
			from = append(from, e.name)
		}
	}
	if len(results) == 0 && !answered {
		return nil, nil, fmt.Errorf("aucun moteur de recherche ne répond pour l'instant (%s) : ne relance pas la recherche ; "+
			"ouvre une URL que tu connais avec web_open, ou réponds avec ce que tu sais en disant que ce n'est pas vérifié",
			strings.Join(why, ", "))
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, from, nil
}

// searchReason : pourquoi un moteur a échoué, en quelques mots (les erreurs
// brutes citaient l'URL de la requête et la phrase système de Windows).
func searchReason(err error) string {
	var b *searchBlocked
	var ue *url.Error
	switch {
	case errors.As(err, &b):
		return b.why
	case errors.Is(err, errSearchResting):
		return "mis de côté un moment"
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "pas de réponse à temps"
	case errors.As(err, &ue):
		return "injoignable"
	}
	return err.Error()
}

// searchURLKey : deux liens vers la même page sont un seul résultat.
func searchURLKey(u string) string {
	u = strings.ToLower(u)
	u, _, _ = strings.Cut(u, "#")
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(strings.TrimPrefix(u, "www."), "/")
}

// searchGet : la page d'un moteur ; une page refusée est un blocage.
func searchGet(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", goFetchUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en;q=0.8")
	for _, c := range consentCookies {
		req.Header.Add("Cookie", c)
	}
	resp, err := goHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, goFetchMaxBytes))
	switch {
	case err != nil:
		return "", err
	case resp.StatusCode == 202 || resp.StatusCode == 403 || resp.StatusCode == 429:
		return "", &searchBlocked{fmt.Sprintf("HTTP %d (refusé)", resp.StatusCode)}
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// ddgEngineSearch : le lecteur DuckDuckGo existant (goSearch), son contrôle
// anti-robot compté comme un blocage.
func ddgEngineSearch(ctx context.Context, q string, limit int) ([]searchResult, error) {
	rs, err := goSearch(q, limit)
	if err != nil && strings.Contains(err.Error(), "anti-bot") {
		return nil, &searchBlocked{"contrôle anti-robot"}
	}
	return rs, err
}

var (
	reSearchTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reSearchNoise = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>|<svg\b.*?</svg>`)
)

func searchInline(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(reSearchTag.ReplaceAllString(s, ""))), " ")
}

func clipSearchSnippet(s string) string {
	if utf8.RuneCountInString(s) <= searchSnippetMax {
		return s
	}
	return string([]rune(s)[:searchSnippetMax]) + " …"
}

// searchLayout : comment lire la page de résultats d'un moteur. Chaque résultat
// est le texte d'une marque de début à la suivante : apparier par position
// décalait tous les résultats dès qu'un seul n'avait pas d'extrait.
type searchLayout struct {
	start, link, snippet *regexp.Regexp
	unwrap               func(string) string
	skip                 func(link string) bool
}

func (l searchLayout) parse(body string, limit int) []searchResult {
	var out []searchResult
	starts := l.start.FindAllStringIndex(body, -1)
	for i, loc := range starts {
		if len(out) >= limit {
			break
		}
		end := len(body)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		block := body[loc[0]:end]
		m := l.link.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		link := l.unwrap(html.UnescapeString(m[1]))
		if l.skip != nil && l.skip(link) || !strings.HasPrefix(link, "http") {
			continue
		}
		r := searchResult{Title: searchInline(m[2]), URL: link}
		if s := l.snippet.FindStringSubmatch(block); s != nil {
			for _, g := range s[1:] {
				if g != "" {
					r.Snippet = clipSearchSnippet(searchInline(g))
					break
				}
			}
		}
		out = append(out, r)
	}
	return out
}

func keepLink(s string) string { return s }

var bingLayout = searchLayout{
	start:   regexp.MustCompile(`<li class="b_algo"`),
	link:    regexp.MustCompile(`(?s)<h2[^>]*>\s*<a\b[^>]*href="([^"]+)"[^>]*>(.*?)</a>`),
	snippet: regexp.MustCompile(`(?s)<p\b[^>]*class="b_lineclamp[^"]*"[^>]*>(.*?)</p>|<div class="b_caption[^"]*"[^>]*>.*?<p\b[^>]*>(.*?)</p>`),
	unwrap:  bingLink,
}

// bingLink : les liens de Bing passent par /ck/a?…&u=a1<l'URL en base64>.
func bingLink(h string) string {
	p, err := url.Parse(h)
	if err != nil || !strings.HasSuffix(p.Host, "bing.com") || p.Path != "/ck/a" {
		return h
	}
	u := p.Query().Get("u")
	if !strings.HasPrefix(u, "a1") {
		return h
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(u[2:], "="))
	if err != nil {
		return h
	}
	return string(b)
}

func bingEngineSearch(ctx context.Context, q string, limit int) ([]searchResult, error) {
	body, err := searchGet(ctx, bingSearchURL+"?q="+url.QueryEscape(q))
	if err != nil {
		return nil, err
	}
	if strings.Contains(body, "challenges.cloudflare.com/turnstile") || strings.Contains(body, `class="captcha"`) {
		return nil, &searchBlocked{"contrôle anti-robot"}
	}
	rs := bingLayout.parse(reSearchNoise.ReplaceAllString(body, ""), limit)
	if len(rs) == 0 { // la page a changé de forme : le flux RSS a les mêmes résultats
		if feed, err := searchGet(ctx, bingSearchURL+"?format=rss&q="+url.QueryEscape(q)); err == nil {
			rs = bingRSS(feed, limit)
		}
	}
	if searchDegraded(q, rs) {
		return nil, &searchBlocked{"résultats qui ignorent la requête"}
	}
	return rs, nil
}

func bingRSS(feed string, limit int) []searchResult {
	var r struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"channel>item"`
	}
	if xml.Unmarshal([]byte(feed), &r) != nil {
		return nil
	}
	var out []searchResult
	for _, it := range r.Items {
		if len(out) < limit && strings.HasPrefix(it.Link, "http") {
			out = append(out, searchResult{searchInline(it.Title), it.Link, clipSearchSnippet(searchInline(it.Description))})
		}
	}
	return out
}

// searchDegraded : des résultats qui ne couvrent aucun mot de la requête sauf le
// premier, comme Bing en donne quand il a vu trop de requêtes. Un des mots après
// le premier doit apparaître dans au moins 3 résultats (titre, extrait ou lien).
// Mesuré : un Bing dégradé faisait 0 sur 10 sur 6 requêtes, un Bing
// normal 10 sur 10.
func searchDegraded(q string, rs []searchResult) bool {
	ws := webSplitWords(q)
	if len(ws) < 3 || len(rs) == 0 {
		return false
	}
	ts := webTerms(strings.Join(ws[1:], " "))
	if len(ts) < 2 {
		return false
	}
	covered := 0
	for _, r := range rs[:min(len(rs), 10)] {
		has := map[string]bool{}
		for _, w := range webWords(r.Title + " " + r.Snippet + " " + r.URL) {
			has[w] = true
		}
		for _, t := range ts {
			if has[t] {
				covered++
				break
			}
		}
	}
	return covered < min(searchEnough, len(rs))
}

var braveLayout = searchLayout{
	start:   regexp.MustCompile(`<div class="snippet[^"]*" data-pos="\d+" data-type="web"`),
	link:    regexp.MustCompile(`(?s)<a href="([^"]+)"[^>]*>.*?<div class="title search-snippet-title[^"]*"[^>]*>(.*?)</div>`),
	snippet: regexp.MustCompile(`(?s)<div class="(?:content|inline-qa-question|inline-qa-answer|product-review)[ "][^>]*>(.*?)</div>`),
	unwrap:  keepLink,
}

func braveEngineSearch(ctx context.Context, q string, limit int) ([]searchResult, error) {
	body, err := searchGet(ctx, braveSearchURL+"?q="+url.QueryEscape(q))
	if err != nil {
		return nil, err
	}
	rs := braveLayout.parse(reSearchNoise.ReplaceAllString(body, ""), limit)
	if len(rs) == 0 && strings.Contains(body, "captcha") {
		return nil, &searchBlocked{"contrôle anti-robot"}
	}
	return rs, nil
}

var reYahooLink = regexp.MustCompile(`/RU=([^/]+)/R[KS]=`)

var yahooLayout = searchLayout{
	start:   regexp.MustCompile(`<div class="[^"]*\balgo-sr\b`),
	link:    regexp.MustCompile(`(?s)<a\b[^>]*href="([^"]+)"[^>]*>.*?<h3\b[^>]*>(.*?)</h3>`),
	snippet: regexp.MustCompile(`(?s)<div class="compText[^"]*"[^>]*>(.*?)</div>`),
	unwrap:  yahooLink,
	skip:    func(l string) bool { return strings.Contains(l, "/cbclk") || strings.Contains(l, "bing.com/aclick") },
}

// yahooLink : les liens de Yahoo passent par r.search.yahoo.com/…/RU=<l'URL échappée>/RK=….
func yahooLink(h string) string {
	if m := reYahooLink.FindStringSubmatch(h); m != nil {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			return u
		}
	}
	return h
}

func yahooEngineSearch(ctx context.Context, q string, limit int) ([]searchResult, error) {
	u := yahooSearchURL + "?p=" + url.QueryEscape(q)
	body, err := searchGet(ctx, u)
	if err != nil && strings.Contains(err.Error(), "EOF") && ctx.Err() == nil { // une rafale coupe la connexion : encore une fois
		body, err = searchGet(ctx, u)
	}
	if err != nil {
		return nil, err
	}
	return yahooLayout.parse(reSearchNoise.ReplaceAllString(body, ""), limit), nil
}

// wikiEngineSearch : Wikipédia en français, puis en anglais quand il y a trop peu.
func wikiEngineSearch(ctx context.Context, q string, limit int) ([]searchResult, error) {
	var out []searchResult
	var failed error
	for _, lang := range []string{"fr", "en"} {
		body, err := searchGet(ctx, fmt.Sprintf(wikiSearchURL, lang)+"?limit=5&q="+url.QueryEscape(q))
		if err != nil {
			failed = err
			continue
		}
		var r struct {
			Pages []struct{ Key, Title, Excerpt, Description string } `json:"pages"`
		}
		if json.Unmarshal([]byte(body), &r) != nil {
			continue
		}
		for _, p := range r.Pages {
			snip := searchInline(p.Excerpt)
			if d := strings.TrimSpace(p.Description); d != "" {
				snip = d + " : " + snip
			}
			out = append(out, searchResult{p.Title, "https://" + lang + ".wikipedia.org/wiki/" + url.PathEscape(p.Key), clipSearchSnippet(snip)})
		}
		if len(out) >= searchEnough {
			break
		}
	}
	if len(out) == 0 && failed != nil {
		return nil, failed
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
