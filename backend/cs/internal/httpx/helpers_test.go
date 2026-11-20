package httpx

import (
	"errors"
	"io"
	"strings"
)

func errPlain(s string) error          { return errors.New(s) }
func stringsReader(s string) io.Reader { return strings.NewReader(s) }
