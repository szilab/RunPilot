package model

import "testing"

func TestNormalizeVNCRemoteOptions(t *testing.T) {
	options, err := NormalizeVNCRemoteOptions(&VNCRemoteOptions{Host: "vnc.example"})
	if err != nil || options.Port != 5900 || options.ConnectTimeoutSeconds != 10 {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	for _, input := range []*VNCRemoteOptions{nil, {}, {Host: "vnc.example:5900"}, {Host: "https://vnc.example"}, {Host: "vnc.example", Port: 65536}, {Host: "vnc.example", ConnectTimeoutSeconds: 121}} {
		if _, err := NormalizeVNCRemoteOptions(input); err == nil {
			t.Fatalf("NormalizeVNCRemoteOptions(%#v) succeeded", input)
		}
	}
}
