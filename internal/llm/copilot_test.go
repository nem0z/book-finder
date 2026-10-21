package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// copilotTestEnv names the model a live test asks through Copilot; the test is skipped without it. It needs
// the Copilot runtime (COPILOT_CLI_PATH) and a signed-in account or a token in LLM_API_KEY.
const copilotTestEnv = "BOOKFINDER_TEST_COPILOT_MODEL"

func TestJSONValue(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                     `{"a":1}`,
		" \n{\"a\":1}\n":              `{"a":1}`,
		"```json\n{\"a\":1}\n```":     `{"a":1}`,
		"```\n{\"a\":1}\n```\nthanks": `{"a":1}`,
		"Sure! {\"a\":1}":             "Sure! {\"a\":1}",
	} {
		if got := jsonValue(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestNewCopilot(t *testing.T) {
	c, err := New(Config{Provider: ProviderCopilot, Model: "m", APIKey: "tok"})
	if cp, ok := c.(*Copilot); err != nil || !ok || cp.model != "m" || cp.token != "tok" {
		t.Errorf("got %+v, %v", c, err)
	}

	if err := NewCopilot("m", "").Close(); err != nil {
		t.Errorf("close before start: %v", err)
	}
}

func TestCopilotLive(t *testing.T) {
	model := os.Getenv(copilotTestEnv)
	if model == "" {
		t.Skipf("%s not set", copilotTestEnv)
	}

	c := NewCopilot(model, os.Getenv("LLM_API_KEY"))
	defer c.Close()

	var a struct {
		City string `json:"city"`
		Year int    `json:"year"`
	}
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"year":{"type":"integer"}},"required":["city","year"],"additionalProperties":false}`)
	if err := c.JSON(context.Background(), "Extract the city and the year.", "We moved to Lisbon in 2019.", "place", schema, &a); err != nil || a.City != "Lisbon" || a.Year != 2019 {
		t.Errorf("got %+v, %v", a, err)
	}
}
