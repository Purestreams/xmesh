package protocol

import (
	"bytes"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	var b bytes.Buffer
	want := Message{Type: TypeTCP, Version: Version, GrantID: "g", Host: "example.com", Port: 443}
	if err := WriteMessage(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&b)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDatagramPreservesBoundariesAndZeroPayload(t *testing.T) {
	var b bytes.Buffer
	want := []Datagram{{Host: "1.1.1.1", Port: 53, Payload: []byte{}}, {Host: "example.com", Port: 9999, Payload: []byte{0, 1, 2, 3}}}
	for _, d := range want {
		if err := WriteDatagram(&b, d); err != nil {
			t.Fatal(err)
		}
	}
	for i := range want {
		got, err := ReadDatagram(&b)
		if err != nil {
			t.Fatal(err)
		}
		if got.Host != want[i].Host || got.Port != want[i].Port || !bytes.Equal(got.Payload, want[i].Payload) {
			t.Fatalf("got %#v want %#v", got, want[i])
		}
	}
}

func TestDatagramRejectsOversizePayload(t *testing.T) {
	err := WriteDatagram(&bytes.Buffer{}, Datagram{Host: "1.1.1.1", Port: 53, Payload: make([]byte, MaxDatagramPayload+1)})
	if err == nil {
		t.Fatal("oversize datagram was accepted")
	}
}
