package ajean

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// Un moteur qui bloque passe la main au suivant et reste de côté ; les doublons
// d'un moteur à l'autre ne comptent qu'une fois.
func TestMultiSearchFallsBack(t *testing.T) {
	calls := map[string]int{}
	mk := func(name string, err error, rs ...searchResult) *searchEngine {
		return &searchEngine{name: name, cool: time.Hour, search: func(ctx context.Context, q string, limit int) ([]searchResult, error) {
			calls[name]++
			return rs, err
		}}
	}
	a := mk("A", &searchBlocked{"contrôle anti-robot"})
	b := mk("B", nil, searchResult{Title: "x", URL: "https://www.ex.com/page/"}, searchResult{Title: "y", URL: "https://ex.com/autre"})
	c := mk("C", nil, searchResult{Title: "x bis", URL: "http://ex.com/page"}, searchResult{Title: "z", URL: "https://z.org"})
	saved := searchChain
	searchChain = func() []*searchEngine { return []*searchEngine{a, b, c} }
	defer func() { searchChain = saved }()

	rs, from, err := multiSearch("une requête", 8)
	if err != nil || len(rs) != 3 || strings.Join(from, ",") != "B,C" {
		t.Fatalf("résultats %v depuis %v (%v)", rs, from, err)
	}
	// A est mis de côté : pas réinterrogé à la requête suivante.
	_, _, _ = multiSearch("autre", 8)
	if calls["A"] != 1 {
		t.Errorf("le moteur bloqué a été réinterrogé %d fois", calls["A"])
	}

	// Aucun moteur ne répond : une erreur qui dit de ne pas relancer.
	d := mk("D", errors.New("panne"))
	searchChain = func() []*searchEngine { return []*searchEngine{d} }
	if _, _, err := multiSearch("q", 8); err == nil || !strings.Contains(err.Error(), "ne relance pas") {
		t.Errorf("attendu une erreur explicite, obtenu %v", err)
	}
}

func TestSearchLayoutsAndLinks(t *testing.T) {
	real := "https://example.org/doc"
	bingHref := "https://www.bing.com/ck/a?!&&p=x&u=a1" + base64.RawURLEncoding.EncodeToString([]byte(real)) + "&ntb=1"
	page := `<li class="b_algo"><h2><a href="` + strings.ReplaceAll(bingHref, "&", "&amp;") + `">Le <b>titre</b></a></h2>` +
		`<div class="b_caption"><p class="b_lineclamp2">Un extrait &amp; du texte</p></div></li>` +
		`<li class="b_algo"><h2><a href="https://deux.org">Deux</a></h2></li>`
	rs := bingLayout.parse(page, 8)
	if len(rs) != 2 || rs[0].URL != real || rs[0].Title != "Le titre" || rs[0].Snippet != "Un extrait & du texte" || rs[1].Snippet != "" {
		t.Fatalf("Bing : %+v", rs)
	}
	if got := yahooLink("https://r.search.yahoo.com/_ylt=x/RU=https%3a%2f%2fex.com%2fa/RK=2/RS=y"); got != "https://ex.com/a" {
		t.Errorf("lien Yahoo : %q", got)
	}
}

// Bing « dégradé » : des résultats qui ne parlent que du premier mot.
func TestSearchDegraded(t *testing.T) {
	game := []searchResult{{Title: "Rust - le jeu de survie", URL: "https://store.jeu.fr/rust"}, {Title: "Rust wiki", URL: "https://rust.fandom.com"}, {Title: "Rust serveurs", URL: "https://servers.net"}}
	if !searchDegraded("rust tokio select example", game) {
		t.Error("résultats sur le seul premier mot : attendu dégradé")
	}
	good := []searchResult{{Title: "tokio::select! macro", URL: "https://docs.rs/tokio"}, {Title: "Using select in Tokio", URL: "https://tokio.rs/select"}, {Title: "select example tokio", URL: "https://ex.org"}}
	if searchDegraded("rust tokio select example", good) {
		t.Error("de vrais résultats ne sont pas dégradés")
	}
}
