package slow

import (
	"testing"

	"store"
)

func TestDescribe(t *testing.T) {
	if got := store.Describe(Driver{}); got != "hello, slow" {
		t.Fatal(got)
	}
}
