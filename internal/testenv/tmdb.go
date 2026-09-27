package testenv

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TMDb IDs of the fake TMDb titles. They match the UAKino test titles.
const (
	EuphoriaTVDB    = 360295
	EuphoriaTMDb    = 85552
	EuphoriaIMDb    = "tt8772296"
	ChainsawTVDB    = 397934
	ChainsawTMDb    = 114410
	ChainsawIMDb    = "tt13616990"
	ShrekTMDb       = 809
	ShrekIMDb       = "tt0298148"
	CrownAffairTMDb = 912
	CrownAffairIMDb = "tt0155267"
)

// FakeTMDb answers the TMDb v3 endpoints the catalog uses with canned JSON.
type FakeTMDb struct {
	mu    sync.Mutex
	count int
}

// Count returns how many requests reached the fake.
func (f *FakeTMDb) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func tvJSON(id, tvdb int, imdb, name, uk, first string, seasons string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"first_air_date":%q,"seasons":[%s],
"external_ids":{"tvdb_id":%d,"imdb_id":%q},
"translations":{"translations":[{"iso_639_1":"en","data":{"name":""}},{"iso_639_1":"uk","iso_3166_1":"UA","data":{"name":%q}}]}}`,
		id, name, first, seasons, tvdb, imdb, uk)
}

func movieJSON(id int, imdb, title, uk, date string) string {
	return fmt.Sprintf(`{"id":%d,"title":%q,"release_date":%q,"imdb_id":%q,
"translations":{"translations":[{"iso_639_1":"uk","iso_3166_1":"UA","data":{"title":%q}}]}}`, id, title, date, imdb, uk)
}

// weekly lists n episodes of a season, one a week from the first air date.
func weekly(season, n int, first string) string {
	d, _ := time.Parse("2006-01-02", first)
	var eps []string
	for i := range n {
		eps = append(eps, fmt.Sprintf(`{"season_number":%d,"episode_number":%d,"air_date":%q}`, season, i+1, d.AddDate(0, 0, 7*i).Format("2006-01-02")))
	}
	return `{"episodes":[` + strings.Join(eps, ",") + `]}`
}

var fakeTMDb = map[string]string{
	fmt.Sprintf("/find/%d?external_source=tvdb_id", EuphoriaTVDB): fmt.Sprintf(`{"tv_results":[{"id":%d}]}`, EuphoriaTMDb),
	fmt.Sprintf("/find/%d?external_source=tvdb_id", ChainsawTVDB): fmt.Sprintf(`{"tv_results":[{"id":%d}]}`, ChainsawTMDb),
	"/find/" + ShrekIMDb + "?external_source=imdb_id":             fmt.Sprintf(`{"movie_results":[{"id":%d}]}`, ShrekTMDb),
	"/find/" + CrownAffairIMDb + "?external_source=imdb_id":       fmt.Sprintf(`{"movie_results":[{"id":%d}]}`, CrownAffairTMDb),
	"/search/tv?query=Euphoria":                                   fmt.Sprintf(`{"results":[{"id":%d}]}`, EuphoriaTMDb),
	"/search/tv?query=Chainsaw Man":                               fmt.Sprintf(`{"results":[{"id":%d}]}`, ChainsawTMDb),
	fmt.Sprintf("/tv/%d", EuphoriaTMDb): tvJSON(EuphoriaTMDb, EuphoriaTVDB, EuphoriaIMDb, "Euphoria", "Ейфорія", "2019-06-16",
		`{"season_number":0,"episode_count":2,"air_date":"2020-12-06"},
{"season_number":1,"episode_count":8,"air_date":"2019-06-16"},
{"season_number":2,"episode_count":8,"air_date":"2022-01-09"},
{"season_number":3,"episode_count":8,"air_date":"2026-04-12"}`),
	fmt.Sprintf("/tv/%d", ChainsawTMDb): tvJSON(ChainsawTMDb, ChainsawTVDB, ChainsawIMDb, "Chainsaw Man", "Людина-бензопила", "2022-10-12",
		`{"season_number":1,"episode_count":12,"air_date":"2022-10-12"}`),
	fmt.Sprintf("/tv/%d/season/1", EuphoriaTMDb): weekly(1, 8, "2019-06-16"),
	fmt.Sprintf("/tv/%d/season/2", EuphoriaTMDb): weekly(2, 8, "2022-01-09"),
	fmt.Sprintf("/tv/%d/season/3", EuphoriaTMDb): weekly(3, 8, "2026-04-12"),
	fmt.Sprintf("/movie/%d", ShrekTMDb):          movieJSON(ShrekTMDb, ShrekIMDb, "Shrek 2", "Шрек 2", "2004-05-19"),
	fmt.Sprintf("/movie/%d", CrownAffairTMDb):    movieJSON(CrownAffairTMDb, CrownAffairIMDb, "The Thomas Crown Affair", "Афера Томаса Крауна", "1999-08-06"),
}

func (f *FakeTMDb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	q := r.URL.Query()
	if q.Get("api_key") != TMDbKey {
		http.Error(w, `{"status_code":7}`, http.StatusUnauthorized)
		return
	}
	key := r.URL.Path
	for _, k := range []string{"external_source", "query"} {
		if v := q.Get(k); v != "" {
			key += "?" + k + "=" + v
		}
	}
	body, ok := fakeTMDb[key]
	if !ok {
		http.Error(w, `{"status_code":34}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}
