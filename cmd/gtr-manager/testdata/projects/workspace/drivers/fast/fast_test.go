package fast

import (
	"testing"

	"store"
)

func TestDescribe(t *testing.T) {
	if got := store.Describe(Driver{}); got != "hello, fast" {
		t.Fatal(got)
	}
}
