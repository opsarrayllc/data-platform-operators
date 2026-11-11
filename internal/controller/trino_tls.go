/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// Trino only authenticates clients over HTTPS, so with an identity provider
// the coordinator gets an HTTPS listener whose certificate comes from a CA the
// operator generates. Clients inside the cluster trust that CA through the
// trino-ca ConfigMap. Plain HTTP stays open for worker traffic (authenticated
// with the internal shared secret) and for TLS-terminating proxies that send
// X-Forwarded-Proto: https; any other HTTP request is refused.
const (
	trinoTLSValidity = 10 * 365 * 24 * time.Hour
	// trinoTLSRenewBefore regenerates the certificate this long before expiry.
	trinoTLSRenewBefore = 30 * 24 * time.Hour
	trinoTLSDir         = "/etc/trino/tls"
	volumeTrinoTLS      = "tls"
	pemTypeCertificate  = "CERTIFICATE"
)

// trinoTLSHosts are the names clients use for the coordinator Service.
func trinoTLSHosts(ns string) []string {
	return []string{
		nameTrino,
		nameTrino + "." + ns,
		nameTrino + "." + ns + ".svc",
		nameTrino + "." + ns + ".svc.cluster.local",
		"localhost",
	}
}

// generateTrinoTLS returns a CA certificate and a PEM keystore (private key,
// server certificate, CA certificate) for the coordinator, as Trino's PEM
// keystore support expects.
func generateTrinoTLS(ns string, now time.Time) (caPEM, keystorePEM []byte, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "data-platform-operator Trino CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(trinoTLSValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: nameTrino + "." + ns + ".svc"},
		DNSNames:              trinoTLSHosts(ns),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(trinoTLSValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: caDER})
	var keystore bytes.Buffer
	keystore.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	keystore.Write(pem.EncodeToMemory(&pem.Block{Type: pemTypeCertificate, Bytes: der}))
	keystore.Write(caPEM)
	return caPEM, keystore.Bytes(), nil
}

func randomSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		// crypto/rand does not fail on supported platforms.
		panic(err)
	}
	return serial
}

// trinoTLSUsable reports whether a stored keystore still fits the namespace
// and is not close to expiry.
func trinoTLSUsable(caPEM, keystorePEM []byte, ns string, now time.Time) bool {
	if len(caPEM) == 0 {
		return false
	}
	for rest := keystorePEM; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if block.Type != pemTypeCertificate {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return false
		}
		// The first certificate is the server's.
		for _, host := range trinoTLSHosts(ns) {
			if !slices.Contains(cert.DNSNames, host) {
				return false
			}
		}
		return now.Add(trinoTLSRenewBefore).Before(cert.NotAfter)
	}
}

// ensureTrinoTLS keeps the coordinator keystore Secret and the public CA
// ConfigMap in place, generating a new CA and certificate when they are
// missing, for another namespace, or about to expire. It returns the CA so the
// caller can restart Trino when it changes.
func (r *DataLakeReconciler) ensureTrinoTLS(ctx context.Context, dp *dataplatformv1alpha1.DataLake, ns string) (string, error) {
	now := time.Now()
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretTrinoTLS, Namespace: ns}, secret)
	if err != nil && !errors.IsNotFound(err) {
		return "", err
	}
	caPEM, keystorePEM := secret.Data[keyTrinoCA], secret.Data[keyTrinoKeystore]
	if errors.IsNotFound(err) || !trinoTLSUsable(caPEM, keystorePEM, ns, now) {
		if caPEM, keystorePEM, err = generateTrinoTLS(ns, now); err != nil {
			return "", fmt.Errorf("generate Trino TLS certificate: %w", err)
		}
	}

	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretTrinoTLS, Namespace: ns}}
	if err := r.apply(ctx, dp, secret, func() error {
		ensureLabels(secret, labelsFor(dp, componentTrinoCoordinator))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{keyTrinoCA: caPEM, keyTrinoKeystore: keystorePEM}
		return nil
	}); err != nil {
		return "", err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapTrinoCA, Namespace: ns}}
	if err := r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labelsFor(dp, componentTrinoCoordinator))
		cm.Data = map[string]string{keyTrinoCA: string(caPEM)}
		return nil
	}); err != nil {
		return "", err
	}
	return string(caPEM), nil
}
