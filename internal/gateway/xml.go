package gateway

import (
	"encoding/xml"
	"io"
)

func xmlEscape(w io.Writer, s string) error { return xml.EscapeText(w, []byte(s)) }
