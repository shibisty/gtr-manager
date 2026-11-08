package main

import (
	"testing"

	"greet"
)

func TestGreet(t *testing.T) {
	if greet.Hello("x") != "hello, x" {
		t.Fatal(greet.Hello("x"))
	}
}
