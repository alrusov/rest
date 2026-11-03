package rest

import (
	"fmt"
	"os"
	"testing"

	"github.com/alrusov/log"
	"github.com/alrusov/misc"
)

//----------------------------------------------------------------------------------------------------------------------------//

func TestMain(m *testing.M) {
	log.Disable()
	os.Exit(m.Run())
}

//----------------------------------------------------------------------------------------------------------------------------//

func Test1(t *testing.T) {
	// TODO
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestParseTime(t *testing.T) {
	s := "2022-01-02T16:17:18.123+03:00"
	expected, err := misc.ParseJSONtime(s)
	if err != nil {
		t.Fatal(err)
	}

	result, err := ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Equal(expected) {
		t.Errorf(`got "%s", expected "%s"`, misc.Time2JSONtz(result), misc.Time2JSONtz(expected))
	}

	s = fmt.Sprintf(`%d`, expected.UnixNano())
	result, err = ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Equal(expected) {
		t.Errorf(`got "%s", expected "%s"`, misc.Time2JSONtz(result), misc.Time2JSONtz(expected))
	}

}

//----------------------------------------------------------------------------------------------------------------------------//
