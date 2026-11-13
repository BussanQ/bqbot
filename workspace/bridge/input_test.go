package main

import (
	"bytes"
	"io"
	"net"
	"testing"
)

func TestReleaseHeldInput(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	r := &rfbInput{Conn: client, width: 1280, height: 800, keys: map[uint32]bool{}}
	done := make(chan error, 1)
	go func() {
		defer client.Close()
		for _, event := range []inputEvent{{Kind: "key", Key: 0xffe3, Down: true}, {Kind: "pointer", X: 1279, Y: 799, Mask: 1}, {Kind: "reset"}} {
			if err := r.apply(event); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	got, err := io.ReadAll(server)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	want := []byte{4, 1, 0, 0, 0, 0, 255, 227, 5, 1, 4, 255, 3, 31, 4, 0, 0, 0, 0, 0, 255, 227, 5, 0, 4, 255, 3, 31}
	if !bytes.Equal(got, want) {
		t.Fatalf("RFB down and release packets: %x", got)
	}
}
