package azure_devops

import (
	"encoding/json"
	"testing"
)

func mustRaw(t *testing.T, vals ...interface{}) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(vals))
	for _, v := range vals {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %v: %v", v, err)
		}
		out = append(out, b)
	}
	return out
}

func TestParseRawDemands_ObjectForm(t *testing.T) {
	raw := mustRaw(t,
		map[string]string{"name": "anka-template", "value": "uuid"},
		map[string]string{"name": "anka-template-tag", "value": "v1"},
	)
	got := parseRawDemands(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 demands, got %d (%+v)", len(got), got)
	}
	if got[0] != (Demand{Name: "anka-template", Value: "uuid"}) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1] != (Demand{Name: "anka-template-tag", Value: "v1"}) {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestParseRawDemands_StringForm(t *testing.T) {
	raw := mustRaw(t,
		"anka-template -equals uuid",
		"anka-template-tag -equals v1",
		"existence-only-cap",
	)
	got := parseRawDemands(raw)
	if len(got) != 3 {
		t.Fatalf("want 3 demands, got %d (%+v)", len(got), got)
	}
	if got[0] != (Demand{Name: "anka-template", Value: "uuid"}) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1] != (Demand{Name: "anka-template-tag", Value: "v1"}) {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2] != (Demand{Name: "existence-only-cap"}) {
		t.Errorf("got[2] = %+v", got[2])
	}
}

func TestParseRawDemands_DropsMalformed(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"name": "anka-template", "value": "uuid"}`),
		json.RawMessage(`{"value": "no-name"}`),       // dropped: name missing
		json.RawMessage(`{"name": ""}`),               // dropped: empty name
		json.RawMessage(`""`),                          // dropped: empty string
		json.RawMessage(`123`),                         // dropped: wrong type
		json.RawMessage(`"agent.os -equals Darwin"`),
	}
	got := parseRawDemands(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 demands after drops, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "anka-template" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Name != "agent.os" || got[1].Value != "Darwin" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestFindJobRequest_PicksHighestRequestID(t *testing.T) {
	requests := []JobRequest{
		{RequestID: 100, JobID: "uuid-a"},
		{RequestID: 200, JobID: "uuid-a"}, // retry, should win
		{RequestID: 50, JobID: "uuid-b"},
	}
	got := FindJobRequest(requests, "uuid-a")
	if got == nil {
		t.Fatal("expected a match for uuid-a")
	}
	if got.RequestID != 200 {
		t.Errorf("got RequestID %d; want 200 (newest retry)", got.RequestID)
	}
}

func TestFindJobRequest_CaseInsensitive(t *testing.T) {
	requests := []JobRequest{
		{RequestID: 1, JobID: "12F1170F-54F2-53F3-20DD-22FC7DFF55F9"},
	}
	got := FindJobRequest(requests, "12f1170f-54f2-53f3-20dd-22fc7dff55f9")
	if got == nil {
		t.Fatal("expected case-insensitive match")
	}
}

func TestFindJobRequest_NoMatch(t *testing.T) {
	if got := FindJobRequest([]JobRequest{{JobID: "a"}}, "b"); got != nil {
		t.Errorf("expected nil for no match, got %+v", got)
	}
}

func TestJobRequest_DemandValue(t *testing.T) {
	r := &JobRequest{Demands: []Demand{
		{Name: "anka-template", Value: "uuid"},
		{Name: "exists-only", Value: ""},
	}}
	if v, ok := r.DemandValue("anka-template"); !ok || v != "uuid" {
		t.Errorf("DemandValue(anka-template) = (%q, %v)", v, ok)
	}
	if v, ok := r.DemandValue("exists-only"); !ok || v != "" {
		t.Errorf("DemandValue(exists-only) = (%q, %v); want (empty, true)", v, ok)
	}
	if v, ok := r.DemandValue("missing"); ok || v != "" {
		t.Errorf("DemandValue(missing) = (%q, %v); want (empty, false)", v, ok)
	}

	var nilReq *JobRequest
	if v, ok := nilReq.DemandValue("anka-template"); ok || v != "" {
		t.Errorf("nil receiver should return empty/false; got (%q, %v)", v, ok)
	}
}
