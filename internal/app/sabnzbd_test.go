package app_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func decodeJSON(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

func TestSABConfigModes(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	dir := e.Config.Paths.Downloads
	tests := []struct {
		mode string
		want string
	}{
		{"version", `{"version":"4.0.0"}`},
		{"fullstatus&skip_dashboard=1", fmt.Sprintf(`{"status":{"completedir":%q}}`, dir)},
		{"get_cats", `{"categories":["sonarr","radarr"]}`},
		{"get_config", fmt.Sprintf(`{"config":{
			"misc":{
				"complete_dir":%q,
				"pre_check":false,
				"enable_tv_sorting":false,"tv_categories":[],
				"enable_movie_sorting":false,"movie_categories":[],
				"enable_date_sorting":false,"date_categories":[],
				"history_retention_option":"all","history_retention_number":0,"history_retention":"0"
			},
			"categories":[{"name":"sonarr","dir":""},{"name":"radarr","dir":""}],
			"servers":[],
			"sorters":[]
		}}`, dir)},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			resp, body := e.Get(t, "/downloader/api?output=json&apikey="+testenv.DownloaderKey+"&mode="+tt.mode)
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("content type %q", ct)
			}
			if got, want := decodeJSON(t, body), decodeJSON(t, tt.want); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s\nwant %s", body, tt.want)
			}
		})
	}
}

func TestSABCompleteDirDefault(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Configure: func(c *config.Config) { c.Paths.Downloads = "/data/downloads" }})
	_, body := e.Get(t, "/downloader/api?output=json&mode=fullstatus&apikey="+testenv.DownloaderKey)
	if want := `{"status":{"completedir":"/data/downloads"}}`; !reflect.DeepEqual(decodeJSON(t, body), decodeJSON(t, want)) {
		t.Fatalf("got %s", body)
	}
}

func TestSABKeyErrors(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"wrong key", "mode=get_config&apikey=wrong", `{"status":false,"error":"API Key Incorrect"}`},
		{"wrong key on version", "mode=version&apikey=wrong", `{"status":false,"error":"API Key Incorrect"}`},
		{"indexer key", "mode=get_config&apikey=" + testenv.IndexerKey, `{"status":false,"error":"API Key Incorrect"}`},
		{"missing key", "mode=get_config", `{"status":false,"error":"API Key Required"}`},
		{"unknown mode", "mode=nope&apikey=" + testenv.DownloaderKey, `{"status":false,"error":"not implemented"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.Get(t, "/downloader/api?output=json&"+tt.query)
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if got, want := decodeJSON(t, body), decodeJSON(t, tt.want); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s\nwant %s", body, tt.want)
			}
		})
	}
}
