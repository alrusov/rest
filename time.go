package rest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразовать строку во время
func ParseTime(s string) (t time.Time, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		err = fmt.Errorf(`time cannot be empty`)
		return
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		t = misc.UnixNano2UTC(n)
		return
	}

	x, err := misc.ParseJSONtime(s)
	if err != nil {
		return
	}

	t = x.UTC()
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
