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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// Trino accepts Kubernetes ServiceAccount tokens as JWTs, so in-cluster
// workloads such as transformation runners can query as themselves. Their
// Trino user is the token subject, system:serviceaccount:<namespace>:<name>,
// which the OPA policy maps to the LakeKeeper principal that
// spec.auth.serviceAccountTokens.grants gives namespace ownership to.
const (
	configMapTrinoJWTKeys = "trino-jwt-keys"
	volumeTrinoJWTKeys    = "jwt-keys"
	trinoJWTKeysDir       = "/etc/trino/jwt-keys"
	authTypeJWT           = "jwt"
	propTrinoAuthType     = "http-server.authentication.type"
	claimSubject          = "sub"
	jwkTypeRSA            = "RSA"
	jwkTypeEC             = "EC"
	curveP256             = "P-256"
	curveP384             = "P-384"
	curveP521             = "P-521"
	// trinoOAuth2UserPattern rejects OIDC subjects that look like
	// ServiceAccount usernames, so no identity provider user can pose as one.
	trinoOAuth2UserPattern = `^(?!system:serviceaccount:)(.+)$`
)

// kidPattern keeps key ids safe as ConfigMap keys and file names.
var kidPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// trinoJWTConfig is the cluster's ServiceAccount token issuer as Trino checks it.
type trinoJWTConfig struct {
	issuer   string
	audience string
}

// ServiceAccountIssuer reads the cluster's ServiceAccount token issuer and its
// public signing keys (a JWKS document).
type ServiceAccountIssuer interface {
	Issuer(ctx context.Context) (issuer string, jwks []byte, err error)
}

type apiServerIssuer struct {
	client rest.Interface
}

// NewServiceAccountIssuer reads the issuer from the API server's discovery
// endpoints, which the system:service-account-issuer-discovery role exposes.
func NewServiceAccountIssuer(cfg *rest.Config) (ServiceAccountIssuer, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &apiServerIssuer{client: dc.RESTClient()}, nil
}

func (a *apiServerIssuer) Issuer(ctx context.Context) (string, []byte, error) {
	raw, err := a.client.Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("read ServiceAccount issuer discovery: %w", err)
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("parse ServiceAccount issuer discovery: %w", err)
	}
	if doc.Issuer == "" {
		return "", nil, errors.New("ServiceAccount issuer discovery has no issuer")
	}
	// The API server always serves the keys here, even when jwks_uri in the
	// discovery document points at an external host.
	jwks, err := a.client.Get().AbsPath("/openid/v1/jwks").DoRaw(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("read ServiceAccount issuer keys: %w", err)
	}
	return doc.Issuer, jwks, nil
}

// trinoJWTEnabled reports whether Trino should accept ServiceAccount tokens:
// clients authenticate at all, and the lake uses ServiceAccount tokens.
func trinoJWTEnabled(oidc oidcConfig) bool {
	return trinoOAuthEnabled(oidc) && oidc.serviceAccounts.enabled
}

// reconcileTrinoJWT publishes the ServiceAccount issuer's keys for Trino and
// returns the issuer settings, or removes the keys when JWT login is off.
func (r *DataLakeReconciler) reconcileTrinoJWT(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
	oidc oidcConfig,
) (*trinoJWTConfig, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapTrinoJWTKeys, Namespace: ns}}
	if !trinoJWTEnabled(oidc) {
		return nil, client.IgnoreNotFound(r.Delete(ctx, cm))
	}
	if r.ServiceAccountIssuer == nil {
		return nil, errors.New("trino: ServiceAccount token login needs the cluster's issuer, but the operator has no issuer client")
	}
	issuer, jwks, err := r.ServiceAccountIssuer.Issuer(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := jwksToPEM(jwks)
	if err != nil {
		return nil, err
	}
	labels := labelsFor(dp, componentTrinoCoordinator)
	if err := r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labels)
		cm.Data = keys
		return nil
	}); err != nil {
		return nil, err
	}
	return &trinoJWTConfig{issuer: issuer, audience: oidc.serviceAccounts.audience}, nil
}

// trinoJWTProperties lets the coordinator accept ServiceAccount tokens next to
// OAuth2. Trino tries the authenticators in order and uses the first that
// accepts the request. The key file is looked up by the token's key id, so key
// rotation only changes the mounted ConfigMap and needs no restart.
func trinoJWTProperties(jwt trinoJWTConfig) map[string]string {
	return map[string]string{
		propTrinoAuthType:                                        authTypeOAuth2 + "," + authTypeJWT,
		"http-server.authentication.jwt.key-file":                trinoJWTKeysDir + "/${KID}.pem",
		"http-server.authentication.jwt.required-issuer":         jwt.issuer,
		"http-server.authentication.jwt.required-audience":       jwt.audience,
		"http-server.authentication.jwt.principal-field":         claimSubject,
		"http-server.authentication.oauth2.user-mapping.pattern": trinoOAuth2UserPattern,
	}
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwksToPEM converts the signing keys in a JWKS document to PEM public keys,
// keyed "<kid>.pem". Keys with unusable ids or types are skipped.
func jwksToPEM(raw []byte) (map[string]string, error) {
	var set struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("parse ServiceAccount issuer keys: %w", err)
	}
	out := map[string]string{}
	for _, k := range set.Keys {
		if !kidPattern.MatchString(k.Kid) || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k.Kid, err)
		}
		if pub == nil {
			continue
		}
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k.Kid, err)
		}
		out[k.Kid+".pem"] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	if len(out) == 0 {
		return nil, errors.New("ServiceAccount issuer publishes no usable signing keys")
	}
	return out, nil
}

// publicKey returns nil for key types Trino cannot use.
func (k jsonWebKey) publicKey() (any, error) {
	switch k.Kty {
	case jwkTypeRSA:
		n, err := decodeBigInt(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decodeBigInt(k.E)
		if err != nil {
			return nil, err
		}
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("invalid RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case jwkTypeEC:
		var curve elliptic.Curve
		switch k.Crv {
		case curveP256:
			curve = elliptic.P256()
		case curveP384:
			curve = elliptic.P384()
		case curveP521:
			curve = elliptic.P521()
		default:
			return nil, nil
		}
		x, err := decodeBigInt(k.X)
		if err != nil {
			return nil, err
		}
		y, err := decodeBigInt(k.Y)
		if err != nil {
			return nil, err
		}
		size := (curve.Params().BitSize + 7) / 8
		if len(x.Bytes()) > size || len(y.Bytes()) > size {
			return nil, errors.New("EC coordinate too large for curve")
		}
		point := make([]byte, 1+2*size)
		point[0] = 4
		x.FillBytes(point[1 : 1+size])
		y.FillBytes(point[1+size:])
		return ecdsa.ParseUncompressedPublicKey(curve, point)
	default:
		return nil, nil
	}
}

func decodeBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(b) == 0 {
		return nil, errors.New("invalid base64url integer")
	}
	return new(big.Int).SetBytes(b), nil
}
