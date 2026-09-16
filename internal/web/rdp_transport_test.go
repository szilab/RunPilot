package web

import (
	"bytes"
	"testing"
)

func TestRDPTunnelOutputRecordsFirstWriteOnce(t *testing.T) {
	var destination bytes.Buffer
	count := 0
	output := &rdpTunnelOutput{writer: &destination, first: func() { count++ }}
	if _, err := output.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if count != 1 || destination.String() != "firstsecond" {
		t.Fatalf("count=%d output=%q", count, destination.String())
	}
}
