package rerank

import "testing"

func TestNew(t *testing.T) {
	if s, name := New(Config{TEIURL: "http://tei", VoyageModel: "m"}); name != "tei http://tei" {
		t.Errorf("without key: %T %q", s, name)
	} else if _, ok := s.(*TEI); !ok {
		t.Errorf("without key: %T", s)
	}

	s, name := New(Config{TEIURL: "http://tei", VoyageAPIKey: "secret"})
	v, ok := s.(*Voyage)
	if !ok || v.model != DefaultVoyageModel || name != "voyage "+DefaultVoyageModel {
		t.Errorf("with key: %T %q", s, name)
	}

	if _, name := New(Config{VoyageAPIKey: "secret", VoyageModel: "rerank-3-lite"}); name != "voyage rerank-3-lite" {
		t.Errorf("model: %q", name)
	}
}
