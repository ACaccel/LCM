package api_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/api"
)

// Live filters (state, health) cannot run in SQL; paging through them must
// still return every match exactly once, in name order.
func TestResourceListPagesThroughLiveFilters(t *testing.T) {
	x := newSite(t)
	var running, registered []string
	for i := range 7 {
		name := fmt.Sprintf("svc%d", i)
		x.createService(serviceDoc(name, ""))
		if i%3 != 0 {
			registered = append(registered, name)
			continue
		}
		if _, err := x.app.Manager.Start(x.ctx, name, startOpts()); err != nil {
			t.Fatal(err)
		}
		running = append(running, name)
	}
	walk := func(query string) (names []string, pages int) {
		t.Helper()
		cursor := ""
		for pages < 20 {
			path := v1 + "/resources?" + query
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			body := x.call("GET", path, nil).want(http.StatusOK).obj()
			pages++
			for _, it := range body["items"].([]any) {
				names = append(names, it.(map[string]any)["name"].(string))
			}
			next, _ := body["nextCursor"].(string)
			if next == "" {
				return names, pages
			}
			cursor = next
		}
		t.Fatalf("%s: paging does not end", query)
		return nil, 0
	}

	// Full pages, and no cursor after the last match.
	if got, pages := walk("state=running&limit=2"); !slices.Equal(got, running) || pages != 2 {
		t.Fatalf("running = %v in %d pages", got, pages)
	}
	if got, pages := walk("state=registered&health=unknown&limit=2"); !slices.Equal(got, registered) || pages != 2 {
		t.Fatalf("registered = %v in %d pages", got, pages)
	}
	if got, pages := walk("health=down&limit=2"); len(got) != 0 || pages != 1 {
		t.Fatalf("down = %v in %d pages", got, pages)
	}

	// A scan budget smaller than the gaps between matches ends pages early
	// (some empty) with a cursor; the walk still sees every match once.
	defer api.SetListScan(1, 2)()
	if got, pages := walk("state=running&limit=1"); !slices.Equal(got, running) || pages != 4 {
		t.Fatalf("running with a small budget = %v in %d pages", got, pages)
	}
	// Without live filters the budget does not apply: pages are exact.
	all := []string{"svc0", "svc1", "svc2", "svc3", "svc4", "svc5", "svc6"}
	if got, pages := walk("limit=3"); !slices.Equal(got, all) || pages != 3 {
		t.Fatalf("all = %v in %d pages", got, pages)
	}
}
