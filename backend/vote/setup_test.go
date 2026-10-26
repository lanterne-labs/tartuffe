package vote

import (
	"encoding/json"
	"testing"
	"time"
)

func TestKeysRoundTrip(t *testing.T) {
	k, err := GenerateKeys(7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Secret(); err != nil {
		t.Fatal(err)
	}
	pk, err := k.Public().ParsedKeys()
	if err != nil {
		t.Fatal(err)
	}
	for j := range pk {
		if !pk[j].IsOnCurve() || pk[j].IsInfinity() {
			t.Fatalf("key %d", j)
		}
	}
	// A proof made for another event does not verify.
	other := k.Public()
	other.Event = 8
	if _, err := other.ParsedKeys(); err == nil {
		t.Fatal("proofs accepted for another event")
	}
}

func TestCanonicalJSON(t *testing.T) {
	b, err := Canonical(struct {
		Z int    `json:"z"`
		A string `json:"a"`
	}{1, "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":"x","z":1}` {
		t.Fatalf("%s", b)
	}
}

func TestSetupValidate(t *testing.T) {
	k, _ := GenerateKeys(1)
	s := Setup{
		V: Version, Event: 1, Version: 1, Blank: 7, NCap: 10, Mask: 0b10001111,
		PK: k.PK, PKPoK: k.PoK, VKHash: "0x01", Circuit: Circuit{SourceHash: "0x02"},
		Relays: []Relay{{URL: "wss://nos.lol", State: "active"}},
		KReal:  DefaultKReal, RMin: DefaultRMin, AnchorAge: DefaultAnchorAge, CheckpointPeriod: DefaultCheckpointPeriod, Validity: DefaultValidity,
		Closes: time.Now().Add(time.Hour),
	}
	for i := 0; i < 10; i++ {
		o := Option{Slot: i, Kind: "reserved"}
		switch {
		case i < 4:
			o = Option{Slot: i, Kind: "candidate", PersonID: int64(i + 1)}
		case i == 7:
			o = Option{Slot: i, Kind: "blank"}
		}
		s.Options = append(s.Options, o)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Mask = 0xff
	if s.Validate() == nil {
		t.Fatal("a mask with reserved slots accepted")
	}
	s.Mask = 0b10001111
	raw, err := Canonical(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Setup
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
}
