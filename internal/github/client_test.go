package github

import (
	"testing"

	"github.com/google/go-github/v74/github"
)

func TestMatchEnterpriseInstallation(t *testing.T) {
	enterpriseID := int64(99)
	orgID := int64(11)
	installations := []*github.Installation{
		{
			ID:         &orgID,
			TargetType: github.Ptr("Organization"),
			Account:    &github.User{Login: github.Ptr("veertuinc")},
		},
		{
			ID:         &enterpriseID,
			TargetType: github.Ptr("Enterprise"),
			Account:    &github.User{Login: github.Ptr("veertu-inc")},
		},
	}

	if got := matchEnterpriseInstallation(installations, "veertu-inc"); got != enterpriseID {
		t.Fatalf("matchEnterpriseInstallation() = %d, want %d", got, enterpriseID)
	}
	if got := matchEnterpriseInstallation(installations, "VEERTU-INC"); got != enterpriseID {
		t.Fatalf("case-insensitive match = %d, want %d", got, enterpriseID)
	}
	if got := matchEnterpriseInstallation(installations, "other-ent"); got != 0 {
		t.Fatalf("unexpected match for other enterprise: %d", got)
	}
	if got := matchEnterpriseInstallation(installations[:1], "veertu-inc"); got != 0 {
		t.Fatalf("org-only list should not match enterprise: %d", got)
	}
}

func TestSummarizeInstallations(t *testing.T) {
	id := int64(151269710)
	got := summarizeInstallations([]*github.Installation{
		{
			ID:         &id,
			TargetType: github.Ptr("Organization"),
			Account:    &github.User{Login: github.Ptr("veertuinc")},
		},
	})
	want := "Organization:veertuinc(id=151269710)"
	if got != want {
		t.Fatalf("summarizeInstallations() = %q, want %q", got, want)
	}
	if got := summarizeInstallations(nil); got != "none" {
		t.Fatalf("summarizeInstallations(nil) = %q, want none", got)
	}
}
