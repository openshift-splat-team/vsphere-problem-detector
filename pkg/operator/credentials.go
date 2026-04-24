package operator

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	corelister "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

const (
	// Component credential secret name and namespace
	componentCredentialSecretName      = "vsphere-diagnostics-creds"
	componentCredentialSecretNamespace = "openshift-config"

	// Shared credential constants (from existing code)
	sharedCredentialSecretName = "vsphere-cloud-credentials"
	sharedCredentialNamespace  = "kube-system"

	// Secret key suffixes
	usernameKeySuffix = ".username"
	passwordKeySuffix = ".password"
)

// CredentialReader provides methods to read vSphere credentials
// with support for component-specific credentials
type CredentialReader struct {
	secretLister corelister.SecretLister
}

// NewCredentialReader creates a new CredentialReader
func NewCredentialReader(secretLister corelister.SecretLister) *CredentialReader {
	return &CredentialReader{
		secretLister: secretLister,
	}
}

// GetCredentialsForVCenter returns credentials for a specific vCenter
// using component-specific credentials with fallback to shared credentials
func (cr *CredentialReader) GetCredentialsForVCenter(vCenterAddress string) (string, string, error) {
	// Try component credentials first
	username, password, err := cr.getComponentCredentials(vCenterAddress)
	if err == nil {
		klog.V(4).Infof("Using component credentials for vCenter %s", vCenterAddress)
		return username, password, nil
	}

	// Log component credential failure and fall back to shared credentials
	if errors.IsNotFound(err) {
		klog.V(4).Infof("Component credentials secret not found, falling back to shared credentials for vCenter %s", vCenterAddress)
	} else {
		klog.V(4).Infof("Failed to read component credentials (%v), falling back to shared credentials for vCenter %s", err, vCenterAddress)
	}

	// Fall back to shared credentials
	username, password, err = cr.getSharedCredentials(vCenterAddress)
	if err != nil {
		return "", "", fmt.Errorf("failed to get credentials for vCenter %s: component credentials unavailable, shared credentials failed: %w", vCenterAddress, err)
	}

	klog.V(4).Infof("Using shared credentials for vCenter %s", vCenterAddress)
	return username, password, nil
}

// getComponentCredentials reads component-specific credentials from openshift-config namespace
func (cr *CredentialReader) getComponentCredentials(vCenterAddress string) (string, string, error) {
	secret, err := cr.secretLister.Secrets(componentCredentialSecretNamespace).Get(componentCredentialSecretName)
	if err != nil {
		return "", "", err
	}

	return cr.extractCredentialsFromSecret(secret, vCenterAddress, componentCredentialSecretName)
}

// getSharedCredentials reads shared credentials from kube-system namespace
func (cr *CredentialReader) getSharedCredentials(vCenterAddress string) (string, string, error) {
	secret, err := cr.secretLister.Secrets(sharedCredentialNamespace).Get(sharedCredentialSecretName)
	if err != nil {
		return "", "", err
	}

	return cr.extractCredentialsFromSecret(secret, vCenterAddress, sharedCredentialSecretName)
}

// extractCredentialsFromSecret extracts username and password from a secret using FQDN-keyed lookup
func (cr *CredentialReader) extractCredentialsFromSecret(secret *corev1.Secret, vCenterAddress, secretName string) (string, string, error) {
	// Construct FQDN-based keys
	userKey := vCenterAddress + usernameKeySuffix
	passwordKey := vCenterAddress + passwordKeySuffix

	// Extract username
	username, ok := secret.Data[userKey]
	if !ok {
		return "", "", fmt.Errorf("secret %q in namespace %q does not contain key %q", secretName, secret.Namespace, userKey)
	}

	// Extract password
	password, ok := secret.Data[passwordKey]
	if !ok {
		return "", "", fmt.Errorf("secret %q in namespace %q does not contain key %q", secretName, secret.Namespace, passwordKey)
	}

	// Validate credentials are non-empty
	if len(username) == 0 {
		return "", "", fmt.Errorf("secret %q in namespace %q has empty username for key %q", secretName, secret.Namespace, userKey)
	}
	if len(password) == 0 {
		return "", "", fmt.Errorf("secret %q in namespace %q has empty password for key %q", secretName, secret.Namespace, passwordKey)
	}

	return string(username), string(password), nil
}

// GetAllVCentersFromSecret returns all vCenter FQDNs found in a secret
// This is useful for enumerating all vCenters when processing multi-vCenter deployments
func (cr *CredentialReader) GetAllVCentersFromSecret(secretName, namespace string) ([]string, error) {
	secret, err := cr.secretLister.Secrets(namespace).Get(secretName)
	if err != nil {
		return nil, err
	}

	vCenters := make(map[string]bool)
	for key := range secret.Data {
		// Extract vCenter FQDN from keys like "vcenter.example.com.username" or "vcenter.example.com.password"
		if len(key) > len(usernameKeySuffix) && key[len(key)-len(usernameKeySuffix):] == usernameKeySuffix {
			vCenter := key[:len(key)-len(usernameKeySuffix)]
			vCenters[vCenter] = true
		} else if len(key) > len(passwordKeySuffix) && key[len(key)-len(passwordKeySuffix):] == passwordKeySuffix {
			vCenter := key[:len(key)-len(passwordKeySuffix)]
			vCenters[vCenter] = true
		}
	}

	result := make([]string, 0, len(vCenters))
	for vCenter := range vCenters {
		result = append(result, vCenter)
	}

	return result, nil
}

// ValidateSecretFormat validates that a secret contains properly formatted credentials
func (cr *CredentialReader) ValidateSecretFormat(secret *corev1.Secret) error {
	if secret == nil {
		return fmt.Errorf("secret is nil")
	}

	if secret.Data == nil || len(secret.Data) == 0 {
		return fmt.Errorf("secret %q in namespace %q has no data", secret.Name, secret.Namespace)
	}

	// Track vCenters with partial credentials
	usernameKeys := make(map[string]bool)
	passwordKeys := make(map[string]bool)

	for key := range secret.Data {
		if len(key) > len(usernameKeySuffix) && key[len(key)-len(usernameKeySuffix):] == usernameKeySuffix {
			vCenter := key[:len(key)-len(usernameKeySuffix)]
			usernameKeys[vCenter] = true
		} else if len(key) > len(passwordKeySuffix) && key[len(key)-len(passwordKeySuffix):] == passwordKeySuffix {
			vCenter := key[:len(key)-len(passwordKeySuffix)]
			passwordKeys[vCenter] = true
		}
	}

	// Check for vCenters with missing username or password
	for vCenter := range usernameKeys {
		if !passwordKeys[vCenter] {
			return fmt.Errorf("secret %q in namespace %q has username but no password for vCenter %q", secret.Name, secret.Namespace, vCenter)
		}
	}

	for vCenter := range passwordKeys {
		if !usernameKeys[vCenter] {
			return fmt.Errorf("secret %q in namespace %q has password but no username for vCenter %q", secret.Name, secret.Namespace, vCenter)
		}
	}

	if len(usernameKeys) == 0 {
		return fmt.Errorf("secret %q in namespace %q contains no valid vCenter credentials", secret.Name, secret.Namespace)
	}

	return nil
}
