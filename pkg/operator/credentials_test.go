package operator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetCredentialsForVCenter(t *testing.T) {
	tests := []struct {
		name              string
		componentSecret   *corev1.Secret
		sharedSecret      *corev1.Secret
		vCenterAddress    string
		wantUsername      string
		wantPassword      string
		wantErr           bool
		wantComponentUsed bool
	}{
		{
			name: "component credentials exist and are used",
			componentSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      componentCredentialSecretName,
					Namespace: componentCredentialSecretNamespace,
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("component-user"),
					"vcenter1.example.com.password": []byte("component-pass"),
				},
			},
			sharedSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sharedCredentialSecretName,
					Namespace: sharedCredentialNamespace,
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("shared-user"),
					"vcenter1.example.com.password": []byte("shared-pass"),
				},
			},
			vCenterAddress:    "vcenter1.example.com",
			wantUsername:      "component-user",
			wantPassword:      "component-pass",
			wantErr:           false,
			wantComponentUsed: true,
		},
		{
			name:            "component credentials missing, fallback to shared",
			componentSecret: nil,
			sharedSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sharedCredentialSecretName,
					Namespace: sharedCredentialNamespace,
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("shared-user"),
					"vcenter1.example.com.password": []byte("shared-pass"),
				},
			},
			vCenterAddress:    "vcenter1.example.com",
			wantUsername:      "shared-user",
			wantPassword:      "shared-pass",
			wantErr:           false,
			wantComponentUsed: false,
		},
		{
			name: "component credentials incomplete, fallback to shared",
			componentSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      componentCredentialSecretName,
					Namespace: componentCredentialSecretNamespace,
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("component-user"),
					// Missing password
				},
			},
			sharedSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sharedCredentialSecretName,
					Namespace: sharedCredentialNamespace,
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("shared-user"),
					"vcenter1.example.com.password": []byte("shared-pass"),
				},
			},
			vCenterAddress:    "vcenter1.example.com",
			wantUsername:      "shared-user",
			wantPassword:      "shared-pass",
			wantErr:           false,
			wantComponentUsed: false,
		},
		{
			name:              "no credentials available",
			componentSecret:   nil,
			sharedSecret:      nil,
			vCenterAddress:    "vcenter1.example.com",
			wantErr:           true,
			wantComponentUsed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with secrets
			var objects []runtime.Object
			if tt.componentSecret != nil {
				objects = append(objects, tt.componentSecret)
			}
			if tt.sharedSecret != nil {
				objects = append(objects, tt.sharedSecret)
			}

			client := fake.NewSimpleClientset(objects...)
			informerFactory := informers.NewSharedInformerFactory(client, 0)
			secretInformer := informerFactory.Core().V1().Secrets()

			// Populate informer cache
			for _, obj := range objects {
				if secret, ok := obj.(*corev1.Secret); ok {
					secretInformer.Informer().GetIndexer().Add(secret)
				}
			}

			cr := NewCredentialReader(secretInformer.Lister())

			gotUsername, gotPassword, err := cr.GetCredentialsForVCenter(tt.vCenterAddress)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetCredentialsForVCenter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if gotUsername != tt.wantUsername {
					t.Errorf("GetCredentialsForVCenter() gotUsername = %v, want %v", gotUsername, tt.wantUsername)
				}
				if gotPassword != tt.wantPassword {
					t.Errorf("GetCredentialsForVCenter() gotPassword = %v, want %v", gotPassword, tt.wantPassword)
				}
			}
		})
	}
}

func TestGetAllVCentersFromSecret(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		wantCount  int
		wantErr    bool
	}{
		{
			name: "single vCenter",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("user1"),
					"vcenter1.example.com.password": []byte("pass1"),
				},
			},
			wantCount: 1,
			wantErr:   false,
		},
		{
			name: "multiple vCenters",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("user1"),
					"vcenter1.example.com.password": []byte("pass1"),
					"vcenter2.example.com.username": []byte("user2"),
					"vcenter2.example.com.password": []byte("pass2"),
					"vcenter3.example.com.username": []byte("user3"),
					"vcenter3.example.com.password": []byte("pass3"),
				},
			},
			wantCount: 3,
			wantErr:   false,
		},
		{
			name: "empty secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{},
			},
			wantCount: 0,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tt.secret)
			informerFactory := informers.NewSharedInformerFactory(client, 0)
			secretInformer := informerFactory.Core().V1().Secrets()
			secretInformer.Informer().GetIndexer().Add(tt.secret)

			cr := NewCredentialReader(secretInformer.Lister())

			vCenters, err := cr.GetAllVCentersFromSecret(tt.secret.Name, tt.secret.Namespace)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetAllVCentersFromSecret() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(vCenters) != tt.wantCount {
				t.Errorf("GetAllVCentersFromSecret() got %d vCenters, want %d", len(vCenters), tt.wantCount)
			}
		})
	}
}

func TestValidateSecretFormat(t *testing.T) {
	tests := []struct {
		name    string
		secret  *corev1.Secret
		wantErr bool
	}{
		{
			name: "valid secret with single vCenter",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("user1"),
					"vcenter1.example.com.password": []byte("pass1"),
				},
			},
			wantErr: false,
		},
		{
			name: "valid secret with multiple vCenters",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("user1"),
					"vcenter1.example.com.password": []byte("pass1"),
					"vcenter2.example.com.username": []byte("user2"),
					"vcenter2.example.com.password": []byte("pass2"),
				},
			},
			wantErr: false,
		},
		{
			name: "invalid secret with missing password",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.username": []byte("user1"),
				},
			},
			wantErr: true,
		},
		{
			name: "invalid secret with missing username",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{
					"vcenter1.example.com.password": []byte("pass1"),
				},
			},
			wantErr: true,
		},
		{
			name: "invalid secret with no data",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Data: map[string][]byte{},
			},
			wantErr: true,
		},
		{
			name:    "nil secret",
			secret:  nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &CredentialReader{}
			err := cr.ValidateSecretFormat(tt.secret)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSecretFormat() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
