// Package defaultini holds the chrome++.ini that Chrome++ 1.20.0 ships,
// src/chrome++.ini of its repository: UTF-16LE with a byte order mark and
// CRLF line endings. Pitstop writes it where an install has none.
package defaultini

import _ "embed"

//go:embed chrome++.ini
var Bytes []byte
