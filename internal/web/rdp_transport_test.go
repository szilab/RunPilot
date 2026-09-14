package web

import "testing"

func TestParseRDCleanPathRequest(t *testing.T) {
	request := derSequence(append(append(derExplicit(0, derInteger(3390)), derExplicit(3, derUTF8("one-time-ticket"))...), derExplicit(6, derOctets([]byte{3, 0, 0, 19}))...))
	parsed, err := parseRDCleanPathRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.proxyAuth != "one-time-ticket" || len(parsed.x224) != 4 {
		t.Fatalf("parsed = %#v", parsed)
	}
	if _, err := parseRDCleanPathRequest([]byte{0x30, 0x01, 0}); err == nil {
		t.Fatal("invalid request was accepted")
	}
}
