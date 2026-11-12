// Package ghrelease finds Chrome++ releases on GitHub.
package ghrelease

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const Repo = "Bush2021/chrome_plus"

// Manifest is the asset of every release that describes its archive. It is
// read from github.com, not GitHub's API: unauthenticated API calls share
// 60 an hour per IP, which users behind a shared proxy or NAT run out of.
const Manifest = "update.json"

type Release struct {
	Tag     string
	Archive Asset
}

type Asset struct {
	Name   string
	Size   int64
	SHA256 string
	URL    string
}

// StatusError is GitHub's answer other than 200 OK.
type StatusError struct {
	URL    string
	Status string
}

func (e *StatusError) Error() string { return "GitHub answered " + e.Status + " for " + e.URL }

type Client struct {
	HTTP *http.Client
	// Base is where the repository is, https://github.com unless a test
	// replaces it.
	Base string
}

type manifest struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

func (c *Client) Latest(ctx context.Context) (*Release, error) {
	releases := cmp.Or(c.Base, "https://github.com") + "/" + Repo + "/releases"
	var m manifest
	if err := c.get(ctx, releases+"/latest/download/"+Manifest, &m); err != nil {
		return nil, err
	}
	if m.Version == "" || strings.ContainsAny(m.Version, "/\\") || m.Size <= 0 {
		return nil, fmt.Errorf("Chrome++ %s is malformed: %+v", Manifest, m)
	}
	if b, err := hex.DecodeString(m.SHA256); err != nil || len(b) != 32 {
		return nil, fmt.Errorf("Chrome++ %s gives no SHA-256 for %s, so it cannot be checked", m.Version, Manifest)
	}
	name := "Chrome++_v" + m.Version + "_x86_x64_arm64.7z"
	return &Release{Tag: m.Version, Archive: Asset{
		Name:   name,
		Size:   m.Size,
		SHA256: strings.ToLower(m.SHA256),
		URL:    releases + "/download/" + url.PathEscape(m.Version) + "/" + url.PathEscape(name),
	}}, nil
}

func (c *Client) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{URL: u, Status: resp.Status}
	}
	return json.Unmarshal(body, v)
}

// Compare orders Chrome++ versions such as "1.18.3" and "1.16.0-alpha.1":
// numbers compare as numbers, and a prerelease sorts before its release.
// A leading "v" is ignored.
func Compare(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	ac, apre, _ := strings.Cut(a, "-")
	bc, bpre, _ := strings.Cut(b, "-")
	if c := compareDotted(ac, bc); c != 0 {
		return c
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return compareDotted(apre, bpre)
}

func compareDotted(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(ap), len(bp)) {
		var x, y string
		if i < len(ap) {
			x = ap[i]
		}
		if i < len(bp) {
			y = bp[i]
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				return cmpInt(xn, yn)
			}
		case x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	return 1
}
