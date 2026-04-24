package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/klog/v2"
)

// PrivilegeCategory represents a category of privileges
type PrivilegeCategory string

const (
	PrivilegeCategoryVCenter    PrivilegeCategory = "vCenter"
	PrivilegeCategoryDatacenter PrivilegeCategory = "Datacenter"
	PrivilegeCategoryDatastore  PrivilegeCategory = "Datastore"
)

// Privilege represents a single vSphere privilege with its category
type Privilege struct {
	ID       string
	Category PrivilegeCategory
}

// GetRequiredDiagnosticsPrivileges returns the list of privileges required for diagnostics operations
// Total: 16 privileges (11 vCenter-level + 1 datacenter-level + 4 datastore-level)
func GetRequiredDiagnosticsPrivileges() []Privilege {
	return []Privilege{
		// vCenter-level privileges (11 total) - for tagging, CNS, sessions, storage profiles
		{ID: "Cns.Searchable", Category: PrivilegeCategoryVCenter},                         // CNS volume operations
		{ID: "ContentLibrary.ReadStorage", Category: PrivilegeCategoryVCenter},             // Content library read
		{ID: "Global.DisableMethods", Category: PrivilegeCategoryVCenter},                  // Global settings read
		{ID: "Global.EnableMethods", Category: PrivilegeCategoryVCenter},                   // Global settings read
		{ID: "Global.Settings", Category: PrivilegeCategoryVCenter},                        // Global configuration
		{ID: "InventoryService.Tagging.ReadTags", Category: PrivilegeCategoryVCenter},      // Tag reading
		{ID: "Sessions.TerminateSession", Category: PrivilegeCategoryVCenter},              // Session management
		{ID: "Sessions.ValidateSession", Category: PrivilegeCategoryVCenter},               // Session validation
		{ID: "StorageProfile.View", Category: PrivilegeCategoryVCenter},                    // Storage profile viewing
		{ID: "System.Anonymous", Category: PrivilegeCategoryVCenter},                       // Anonymous access
		{ID: "System.View", Category: PrivilegeCategoryVCenter},                            // System view

		// Datacenter-level privilege (1 total)
		{ID: "System.Read", Category: PrivilegeCategoryDatacenter},                         // System read operations

		// Datastore-level privileges (4 total) - for read-only datastore checks
		{ID: "Datastore.Browse", Category: PrivilegeCategoryDatastore},                     // Browse datastore
		{ID: "Datastore.FileManagement", Category: PrivilegeCategoryDatastore},             // File management (read operations)
		{ID: "Host.Config.Storage", Category: PrivilegeCategoryDatastore},                  // Host storage config read
		{ID: "StoragePod.Config", Category: PrivilegeCategoryDatastore},                    // Storage pod config read
	}
}

// PrivilegeValidator validates vSphere privileges
type PrivilegeValidator struct {
	client *vim25.Client
}

// NewPrivilegeValidator creates a new PrivilegeValidator
func NewPrivilegeValidator(client *vim25.Client) *PrivilegeValidator {
	return &PrivilegeValidator{
		client: client,
	}
}

// ValidateDiagnosticsPrivileges validates that the current session has required diagnostics privileges
// Returns a list of missing privileges or nil if all privileges are present
func (pv *PrivilegeValidator) ValidateDiagnosticsPrivileges(ctx context.Context) ([]string, error) {
	requiredPrivileges := GetRequiredDiagnosticsPrivileges()

	// Get current session to find the authenticated user
	sessionMgr := session.NewManager(pv.client)
	user, err := pv.retryGetUserSession(ctx, sessionMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to get user session: %w", err)
	}

	klog.V(4).Infof("Validating privileges for user: %s", user.UserName)

	// Check privileges
	missing, err := pv.checkPrivileges(ctx, user.UserName, requiredPrivileges)
	if err != nil {
		return nil, fmt.Errorf("failed to check privileges: %w", err)
	}

	if len(missing) > 0 {
		klog.V(2).Infof("Missing %d privileges for diagnostics operations", len(missing))
		return missing, nil
	}

	klog.V(4).Infof("All required diagnostics privileges validated successfully")
	return nil, nil
}

// checkPrivileges checks if the user has all required privileges
func (pv *PrivilegeValidator) checkPrivileges(ctx context.Context, userName string, requiredPrivileges []Privilege) ([]string, error) {
	// Get the authorization manager
	authMgr := pv.client.ServiceContent.AuthorizationManager
	if authMgr == nil {
		return nil, fmt.Errorf("authorization manager not available")
	}

	// Build list of privilege IDs to check
	privilegeIDs := make([]string, len(requiredPrivileges))
	for i, priv := range requiredPrivileges {
		privilegeIDs[i] = priv.ID
	}

	// Call HasPrivilegeOnEntities to check privileges
	// Note: We check against the root folder which represents vCenter-level privileges
	req := types.HasPrivilegeOnEntities{
		This:   *authMgr,
		Entity: []types.ManagedObjectReference{pv.client.ServiceContent.RootFolder},
		SessionId: userName,
		PrivId: privilegeIDs,
	}

	res, err := pv.retryHasPrivilegeOnEntities(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to check privileges: %w", err)
	}

	// Identify missing privileges
	var missing []string
	if len(res.Returnval) > 0 {
		entityPrivileges := res.Returnval[0]
		for i, hasPrivilege := range entityPrivileges.PrivAvailability {
			if !hasPrivilege.IsGranted {
				missing = append(missing, privilegeIDs[i])
			}
		}
	}

	return missing, nil
}

// retryGetUserSession retries getting user session with exponential backoff for transient errors
func (pv *PrivilegeValidator) retryGetUserSession(ctx context.Context, sessionMgr *session.Manager) (*types.UserSession, error) {
	maxRetries := 3
	backoff := 100 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			klog.V(4).Infof("Retrying get user session (attempt %d/%d)", attempt+1, maxRetries)
			time.Sleep(backoff)
			backoff *= 2
		}

		user, err := sessionMgr.UserSession(ctx)
		if err == nil {
			return user, nil
		}

		lastErr = err
		if !isTransientError(err) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// retryHasPrivilegeOnEntities retries privilege check with exponential backoff for transient errors
func (pv *PrivilegeValidator) retryHasPrivilegeOnEntities(ctx context.Context, req types.HasPrivilegeOnEntities) (*types.HasPrivilegeOnEntitiesResponse, error) {
	maxRetries := 3
	backoff := 100 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			klog.V(4).Infof("Retrying privilege check (attempt %d/%d)", attempt+1, maxRetries)
			time.Sleep(backoff)
			backoff *= 2
		}

		res, err := methods.HasPrivilegeOnEntities(ctx, pv.client, &req)
		if err == nil {
			return res, nil
		}

		lastErr = err
		if !isTransientError(err) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// isTransientError determines if an error is transient and should be retried
func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	transientErrors := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"temporary failure",
		"503 Service Unavailable",
		"502 Bad Gateway",
	}

	for _, transient := range transientErrors {
		if strings.Contains(strings.ToLower(errStr), strings.ToLower(transient)) {
			return true
		}
	}

	return false
}

// FormatMissingPrivilegesError formats a user-friendly error message for missing privileges
func FormatMissingPrivilegesError(missing []string) string {
	if len(missing) == 0 {
		return ""
	}

	return fmt.Sprintf("missing required diagnostics privileges (%d total): %s",
		len(missing),
		strings.Join(missing, ", "))
}

// GetPrivilegeCategories returns a map of privilege IDs to their categories
func GetPrivilegeCategories() map[string]PrivilegeCategory {
	privileges := GetRequiredDiagnosticsPrivileges()
	categories := make(map[string]PrivilegeCategory)
	for _, priv := range privileges {
		categories[priv.ID] = priv.Category
	}
	return categories
}
