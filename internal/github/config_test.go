package github

import (
	"encoding/json"
	"testing"
)

func TestRepositoryUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantName  string
		wantOwner string
		wantNil   bool
		wantErr   bool
	}{
		{
			name:      "owner as string (Redis queue format)",
			input:     `{"name":"my-repo","owner":"Hotmart-Org","visibility":"private","private":true}`,
			wantName:  "my-repo",
			wantOwner: "Hotmart-Org",
		},
		{
			name:      "owner as object (GitHub webhook payload format)",
			input:     `{"name":"my-repo","owner":{"login":"Hotmart-Org","id":12345,"node_id":"MDEyOk9yZ2FuaXphdGlvbjEyMzQ1"},"visibility":"private","private":true}`,
			wantName:  "my-repo",
			wantOwner: "Hotmart-Org",
		},
		{
			name:    "owner is null",
			input:   `{"name":"my-repo","owner":null}`,
			wantNil: true,
		},
		{
			name:    "owner is absent",
			input:   `{"name":"my-repo"}`,
			wantNil: true,
		},
		{
			name:    "owner as invalid type",
			input:   `{"name":"my-repo","owner":12345}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var repo Repository
			err := json.Unmarshal([]byte(tt.input), &repo)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if repo.Owner != nil {
					t.Fatalf("expected Owner to be nil, got %q", *repo.Owner)
				}
				return
			}
			if repo.Owner == nil {
				t.Fatal("expected Owner to be non-nil")
			}
			if *repo.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", *repo.Owner, tt.wantOwner)
			}
			if repo.Name == nil || *repo.Name != tt.wantName {
				t.Errorf("Name = %v, want %q", repo.Name, tt.wantName)
			}
		})
	}
}

func TestRepositoryMarshalJSON(t *testing.T) {
	owner := "Hotmart-Org"
	name := "my-repo"
	repo := Repository{
		Name:  &name,
		Owner: &owner,
	}
	data, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var roundTripped Repository
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal() roundtrip error = %v", err)
	}
	if roundTripped.Owner == nil || *roundTripped.Owner != owner {
		t.Errorf("roundtrip Owner = %v, want %q", roundTripped.Owner, owner)
	}
}
