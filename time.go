package api

import (
	"strconv"
	"time"

	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

// Преобразовать строку во время
func ParseTime(s string) (t time.Time, err error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		t = misc.UnixNano2UTC(n)
		return
	}

	x, err := misc.ParseJSONtime(s)
	if err != nil {
		return
	}

	t = x
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
