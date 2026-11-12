package ghrelease

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sum = "bfcca14dd07fc9823a639f4a46b7c53ce11fc86ef1da19eab818c800cf49a159"

func server(t *testing.T, manifest string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /Bush2021/chrome_plus/releases/latest/download/update.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Bush2021/chrome_plus/releases/download/1.18.3/update.json", http.StatusFound)
	})
	mux.HandleFunc("GET /Bush2021/chrome_plus/releases/download/1.18.3/update.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(manifest))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatest(t *testing.T) {
	srv := server(t, `{"version": "1.18.3", "size": 181011, "sha256": "`+strings.ToUpper(sum)+`"}`)
	r, err := (&Client{Base: srv.URL}).Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Asset{
		Name:   "Chrome++_v1.18.3_x86_x64_arm64.7z",
		Size:   181011,
		SHA256: sum,
		URL:    srv.URL + "/Bush2021/chrome_plus/releases/download/1.18.3/Chrome++_v1.18.3_x86_x64_arm64.7z",
	}
	if r.Tag != "1.18.3" || r.Archive != want {
		t.Errorf("%+v", r)
	}
}

func TestLatestRefuses(t *testing.T) {
	for name, manifest := range map[string]string{
		"no hash":    `{"version": "1.18.3", "size": 181011}`,
		"short hash": `{"version": "1.18.3", "size": 181011, "sha256": "bfcca14d"}`,
		"no size":    `{"version": "1.18.3", "sha256": "` + sum + `"}`,
		"a path":     `{"version": "../1.18.3", "size": 181011, "sha256": "` + sum + `"}`,
	} {
		srv := server(t, manifest)
		if r, err := (&Client{Base: srv.URL}).Latest(context.Background()); err == nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

// A release without a manifest, such as one published before Pitstop read
// manifests, says so instead of offering nothing.
func TestLatestWithoutAManifest(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err := (&Client{Base: srv.URL}).Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "update.json") {
		t.Errorf("Latest = %v", err)
	}
	if s, ok := errors.AsType[*StatusError](err); !ok || !strings.HasPrefix(s.Status, "404") {
		t.Errorf("StatusError = %+v", s)
	}
}

func TestCompare(t *testing.T) {
	ordered := []string{"1.9.9", "1.16.0-alpha.1", "1.16.0-alpha.2", "1.16.0-alpha.10", "1.16.0", "v1.16.1", "1.18.3", "1.18.4", "2.0.0"}
	for i := range ordered {
		for j := range ordered {
			got := Compare(ordered[i], ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}
