package operator

import (
	"strings"
	"testing"
)

func TestGetRequiredDiagnosticsPrivileges(t *testing.T) {
	privileges := GetRequiredDiagnosticsPrivileges()

	// Verify total count (~16 privileges)
	expectedCount := 16
	if len(privileges) != expectedCount {
		t.Errorf("GetRequiredDiagnosticsPrivileges() returned %d privileges, want %d", len(privileges), expectedCount)
	}

	// Verify privilege categories
	vCenterCount := 0
	datacenterCount := 0
	datastoreCount := 0

	for _, priv := range privileges {
		switch priv.Category {
		case PrivilegeCategoryVCenter:
			vCenterCount++
		case PrivilegeCategoryDatacenter:
			datacenterCount++
		case PrivilegeCategoryDatastore:
			datastoreCount++
		default:
			t.Errorf("Unknown privilege category: %s", priv.Category)
		}
	}

	// Expected counts based on requirements
	if vCenterCount != 11 {
		t.Errorf("Expected 11 vCenter-level privileges, got %d", vCenterCount)
	}
	if datacenterCount != 1 {
		t.Errorf("Expected 1 datacenter-level privilege, got %d", datacenterCount)
	}
	if datastoreCount != 4 {
		t.Errorf("Expected 4 datastore-level privileges, got %d", datastoreCount)
	}
}

func TestGetRequiredDiagnosticsPrivileges_AllCategories(t *testing.T) {
	privileges := GetRequiredDiagnosticsPrivileges()

	// Verify all three categories are represented
	hasVCenter := false
	hasDatacenter := false
	hasDatastore := false

	for _, priv := range privileges {
		switch priv.Category {
		case PrivilegeCategoryVCenter:
			hasVCenter = true
		case PrivilegeCategoryDatacenter:
			hasDatacenter = true
		case PrivilegeCategoryDatastore:
			hasDatastore = true
		}
	}

	if !hasVCenter {
		t.Error("Missing vCenter-level privileges")
	}
	if !hasDatacenter {
		t.Error("Missing datacenter-level privileges")
	}
	if !hasDatastore {
		t.Error("Missing datastore-level privileges")
	}
}

func TestGetRequiredDiagnosticsPrivileges_NoDuplicates(t *testing.T) {
	privileges := GetRequiredDiagnosticsPrivileges()

	seen := make(map[string]bool)
	for _, priv := range privileges {
		if seen[priv.ID] {
			t.Errorf("Duplicate privilege ID found: %s", priv.ID)
		}
		seen[priv.ID] = true
	}
}

func TestFormatMissingPrivilegesError(t *testing.T) {
	tests := []struct {
		name          string
		missing       []string
		wantContains  []string
		wantEmpty     bool
	}{
		{
			name:    "single missing privilege",
			missing: []string{"System.Read"},
			wantContains: []string{
				"missing required diagnostics privileges",
				"1 total",
				"System.Read",
			},
			wantEmpty: false,
		},
		{
			name:    "multiple missing privileges",
			missing: []string{"System.Read", "Datastore.Browse", "Cns.Searchable"},
			wantContains: []string{
				"missing required diagnostics privileges",
				"3 total",
				"System.Read",
				"Datastore.Browse",
				"Cns.Searchable",
			},
			wantEmpty: false,
		},
		{
			name:      "no missing privileges",
			missing:   []string{},
			wantEmpty: true,
		},
		{
			name:      "nil missing privileges",
			missing:   nil,
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatMissingPrivilegesError(tt.missing)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("FormatMissingPrivilegesError() = %q, want empty string", result)
				}
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("FormatMissingPrivilegesError() = %q, should contain %q", result, want)
				}
			}
		})
	}
}

func TestGetPrivilegeCategories(t *testing.T) {
	categories := GetPrivilegeCategories()

	privileges := GetRequiredDiagnosticsPrivileges()
	if len(categories) != len(privileges) {
		t.Errorf("GetPrivilegeCategories() returned %d categories, want %d", len(categories), len(privileges))
	}

	// Verify each privilege has a category
	for _, priv := range privileges {
		category, ok := categories[priv.ID]
		if !ok {
			t.Errorf("Privilege %s not found in category map", priv.ID)
			continue
		}
		if category != priv.Category {
			t.Errorf("Privilege %s has category %s in map, want %s", priv.ID, category, priv.Category)
		}
	}
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantRetry   bool
	}{
		{
			name:      "nil error",
			err:       nil,
			wantRetry: false,
		},
		{
			name:      "connection refused error",
			err:       &fakeError{msg: "connection refused"},
			wantRetry: true,
		},
		{
			name:      "connection reset error",
			err:       &fakeError{msg: "connection reset by peer"},
			wantRetry: true,
		},
		{
			name:      "timeout error",
			err:       &fakeError{msg: "context deadline exceeded (timeout)"},
			wantRetry: true,
		},
		{
			name:      "503 service unavailable error",
			err:       &fakeError{msg: "HTTP 503 Service Unavailable"},
			wantRetry: true,
		},
		{
			name:      "502 bad gateway error",
			err:       &fakeError{msg: "HTTP 502 Bad Gateway"},
			wantRetry: true,
		},
		{
			name:      "permanent authentication error",
			err:       &fakeError{msg: "invalid credentials"},
			wantRetry: false,
		},
		{
			name:      "permanent not found error",
			err:       &fakeError{msg: "object not found"},
			wantRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientError(tt.err)
			if got != tt.wantRetry {
				t.Errorf("isTransientError(%v) = %v, want %v", tt.err, got, tt.wantRetry)
			}
		})
	}
}

// fakeError is a simple error implementation for testing
type fakeError struct {
	msg string
}

func (e *fakeError) Error() string {
	return e.msg
}
