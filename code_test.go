package main

import (
	"bytes"
	"testing"
)

func TestCodeRoundTrip(t *testing.T) {
	in := NetCode{Key: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, Endpoints: []string{
		"192.168.1.5:8750", "203.0.113.7:8750", "[2001:db8::1]:8750",
		"https://brave-silly-words-here.trycloudflare.com", "https://example.com",
	}}
	code := encodeCode(in)
	out, err := decodeCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in.Key, out.Key) || len(out.Endpoints) != len(in.Endpoints) {
		t.Fatalf("mismatch: %v", out)
	}
	for i := range in.Endpoints {
		if in.Endpoints[i] != out.Endpoints[i] {
			t.Fatalf("endpoint %d: %q != %q", i, in.Endpoints[i], out.Endpoints[i])
		}
	}
	// lower-case, spaces and look-alike letters still decode
	if _, err := decodeCode(" sm-" + code[3:] + " "); err != nil {
		t.Fatal(err)
	}
	t.Log(code, len(code))
}

func TestVersionCompare(t *testing.T) {
	if !newer("v1.2.0", "1.1.9") || newer("v1.0.0", "1.0.0") || newer("1.0.0", "1.0.1") {
		t.Fatal("version compare broken")
	}
}
