package pitstop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/Bush2021/chrome_update/fetch"
	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/ghrelease"
)

// dropped is the error of a request to a server that closes every
// connection unanswered: Post "...": EOF.
func dropped(t *testing.T) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, err = http.Post("http://"+ln.Addr().String()+"/service/update2/json", "text/xml", nil)
	if err == nil {
		t.Fatal("got no error")
	}
	return err
}

func TestExplain(t *testing.T) {
	status := &fetch.StatusError{URL: "https://dl.google.com/x.crx3", Code: 503, Status: "503 Service Unavailable"}
	for _, c := range []struct {
		name    string
		err     error
		trouble Trouble
		host    string
	}{
		{"dropped", fmt.Errorf("checking Chrome: %w", dropped(t)), NetworkTrouble, "127.0.0.1"},
		{"stall", fmt.Errorf("x.crx3: %w", fmt.Errorf("https://dl.google.com/x.crx3: no data for 30s: %w", os.ErrDeadlineExceeded)), NetworkTrouble, ""},
		{"cut short", fmt.Errorf("x.crx3: %w", io.ErrUnexpectedEOF), NetworkTrouble, ""},
		{"download status", fmt.Errorf("x.crx3: %w", errors.Join(status)), ServerTrouble, "dl.google.com"},
		{"network beats status", errors.Join(status, fmt.Errorf("y: %w", os.ErrDeadlineExceeded)), NetworkTrouble, ""},
		{"GitHub status", &ghrelease.StatusError{URL: "https://github.com/a/b/update.json", Status: "404 Not Found"}, ServerTrouble, "github.com"},
		{"Omaha status", fmt.Errorf("checking: %w", &omaha.StatusError{Status: "429 Too Many Requests"}), ServerTrouble, ""},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), "", ""},
		{"other", errors.New("disk full"), "", ""},
		{"nil", nil, "", ""},
	} {
		trouble, host := Explain(c.err)
		if trouble != c.trouble || host != c.host {
			t.Errorf("%s: Explain(%v) = %q, %q; want %q, %q", c.name, c.err, trouble, host, c.trouble, c.host)
		}
	}
}
