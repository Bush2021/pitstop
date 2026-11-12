package pitstop

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"

	"github.com/Bush2021/chrome_update/fetch"
	"github.com/Bush2021/chrome_update/omaha"
	"github.com/Bush2021/pitstop/internal/ghrelease"
)

// Trouble is what an error means to someone who can't read it: their
// network failed, or a server did. Other errors have none.
type Trouble string

const (
	NetworkTrouble Trouble = "network"
	ServerTrouble  Trouble = "server"
)

// Explain says whose trouble err is and which host it came from, if known.
// A download tries several hosts; when one failed on the network, that is
// what the user can act on, so it wins over a server's answer.
func Explain(err error) (Trouble, string) {
	if err == nil || errors.Is(err, context.Canceled) {
		return "", ""
	}
	if _, ok := errors.AsType[net.Error](err); ok || errors.Is(err, io.ErrUnexpectedEOF) {
		host := ""
		if u, ok := errors.AsType[*url.Error](err); ok {
			host = hostOf(u.URL)
		}
		return NetworkTrouble, host
	}
	if s, ok := errors.AsType[*fetch.StatusError](err); ok {
		return ServerTrouble, hostOf(s.URL)
	}
	if s, ok := errors.AsType[*ghrelease.StatusError](err); ok {
		return ServerTrouble, hostOf(s.URL)
	}
	if _, ok := errors.AsType[*omaha.StatusError](err); ok {
		return ServerTrouble, ""
	}
	return "", ""
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}
