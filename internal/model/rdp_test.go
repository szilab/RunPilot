package model

import "testing"

func TestNormalizeRDPRemoteOptions(t *testing.T) {
	options, err := NormalizeRDPRemoteOptions(&RDPRemoteOptions{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Port != 3389 || options.SecurityMode != RDPSecurityAutomatic || options.Clipboard == nil || !*options.Clipboard || options.DynamicResize == nil || *options.DynamicResize {
		t.Fatalf("defaults = %#v", options)
	}
	for _, input := range []*RDPRemoteOptions{
		{Host: ""},
		{Host: "rdp://server"},
		{Host: "server:3389"},
		{Host: "server", Port: 65536},
		{Host: "server", SecurityMode: "unsupported"},
	} {
		if _, err := NormalizeRDPRemoteOptions(input); err == nil {
			t.Fatalf("NormalizeRDPRemoteOptions(%#v) succeeded", input)
		}
	}
}
