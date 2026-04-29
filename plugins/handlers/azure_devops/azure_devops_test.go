package azure_devops

import "testing"

// resolveTemplate is exercised end-to-end at runtime against a live ADO
// API; testing the full function in unit tests would require a non-trivial
// HTTP fake and would mostly verify ListJobRequests' wire format. The
// demand parsing and request matching that resolveTemplate depends on are
// covered by tests in the internal/azure_devops package.
//
// What we do test here: that the demand-name constants stay in sync with
// the dashes-form ADO convention - underscores would silently fail to
// match the YAML demands an operator wrote.

func TestDemandConstantsUseDashes(t *testing.T) {
	if demandAnkaTemplate != "anka-template" {
		t.Errorf("demandAnkaTemplate = %q; ADO convention is dashes", demandAnkaTemplate)
	}
	if demandAnkaTemplateTag != "anka-template-tag" {
		t.Errorf("demandAnkaTemplateTag = %q; ADO convention is dashes", demandAnkaTemplateTag)
	}
}
