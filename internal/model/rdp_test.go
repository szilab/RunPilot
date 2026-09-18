package model

import "testing"

func TestNormalizeRDPRemoteOptions(t *testing.T) {
	options, err := NormalizeRDPRemoteOptions(&RDPRemoteOptions{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Port != 3389 || options.SecurityMode != RDPSecurityAutomatic || options.Clipboard == nil || !*options.Clipboard || options.DynamicResize == nil || !*options.DynamicResize || options.ResizeMethod != "display-update" {
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

func TestNormalizeGuacdConfig(t *testing.T) {
	got, err := NormalizeGuacdConfig(GuacdConfig{})
	if err != nil || got.Host != "127.0.0.1" || got.Port != 4822 || got.ConnectTimeoutSeconds != 5 {
		t.Fatalf("defaults=%#v err=%v", got, err)
	}
	for _, value := range []GuacdConfig{{Host: "http://bad"}, {Host: "host:4822"}, {Port: 65536}, {ConnectTimeoutSeconds: 61}} {
		if _, err := NormalizeGuacdConfig(value); err == nil {
			t.Fatalf("accepted %#v", value)
		}
	}
}
