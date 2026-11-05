package message_test

import (
	"encoding/json"
	"testing"

	"github.com/ACaccel/lcm/internal/message"
)

func TestNewAndJSON(t *testing.T) {
	m := message.New("step.lock", "lock resource:db", "lock", "resource:db")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"code":"step.lock","params":{"lock":"resource:db"},"message":"lock resource:db"}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	b, err = json.Marshal(message.Text("free text"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"code":"","params":{},"message":"free text"}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if got := message.Texts([]message.Message{m}); len(got) != 1 || got[0] != "lock resource:db" {
		t.Fatalf("Texts = %v", got)
	}
}
