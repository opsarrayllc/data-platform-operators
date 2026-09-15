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
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

const (
	sampleDataSQLKey    = "sample.sql"
	sampleDataScriptKey = "seed.py"
	sampleDataMountPath = "/sample"
	sampleDataHashAnno  = "dataplatform.opsarray.io/sample-data-hash"
	uidSampleData       = int64(65532)
	gidSampleData       = int64(65532)
)

func (r *DataPlatformReconciler) reconcileSampleData(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	oidc oidcConfig,
) (bool, error) {
	if !dp.Spec.Trino.IsEnabled() || !dp.Spec.Lakekeeper.IsEnabled() || !dp.Spec.SampleData.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionTrue, reasonDisabled, "Sample data is disabled")
		return false, nil
	}
	if dp.Spec.Auth.IsEnabled() && !dp.Spec.Auth.IsEmbedded() {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionTrue, reasonDisabled,
			"Sample data skipped: embedded Keycloak is required to authenticate the seed Job")
		return false, nil
	}
	if !conditionTrue(dp, dataplatformv1alpha1.ConditionWarehouseReady) {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionFalse, reasonNotReady, "Waiting for warehouse")
		return true, nil
	}
	if !conditionTrue(dp, dataplatformv1alpha1.ConditionTrinoReady) {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionFalse, reasonNotReady, "Waiting for Trino")
		return true, nil
	}

	ns := dp.Spec.Trino.NamespaceOrDefault()
	schema := dp.Spec.SampleData.SchemaOrDefault()
	catalog := dataplatformv1alpha1.DefaultTrinoCatalog
	sql := sampleDataSQL(catalog, schema)
	script := sampleDataSeedScript()
	cmHash := hashData(sql, script)

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapSampleData, Namespace: ns}}
	labels := labelsFor(dp, componentSampleData)
	if err := r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labels)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[sampleDataSQLKey] = sql
		cm.Data[sampleDataScriptKey] = script
		return nil
	}); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionFalse, reasonError, err.Error())
		return false, err
	}

	needsOAuth := trinoOAuthEnabled(oidc, dp.Spec.Trino.PublicURL, dp.Spec.Superset.IsEnabled())
	ready, err := r.ensureSampleDataJob(ctx, dp, ns, cmHash, oidc, needsOAuth)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionFalse, reasonError, err.Error())
		return false, err
	}
	if !ready {
		setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionFalse, reasonNotReady, "Sample data Job is running")
		return true, nil
	}
	setCondition(dp, dataplatformv1alpha1.ConditionSampleDataReady, metav1.ConditionTrue, reasonReady,
		fmt.Sprintf("Seeded %s.%s.orders and %s.%s.invoices", catalog, schema, catalog, schema))
	return false, nil
}

func (r *DataPlatformReconciler) ensureSampleDataJob(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, cmHash string,
	oidc oidcConfig,
	needsOAuth bool,
) (bool, error) {
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: nameSampleDataJob, Namespace: ns}, job)
	if err == nil {
		if !job.DeletionTimestamp.IsZero() {
			return false, nil
		}
		if jobSucceeded(job) {
			return true, nil
		}
		if jobFailed(job) || sampleDataJobHash(job) != cmHash {
			if delErr := r.Delete(ctx, job); delErr != nil {
				return false, delErr
			}
			return false, nil
		}
		return false, nil
	}
	if !errors.IsNotFound(err) {
		return false, err
	}

	env := []corev1.EnvVar{
		{Name: "TRINO_URL", Value: clusterServiceURL(nameTrino, ns, trinoPort)},
		{Name: "SQL_FILE", Value: sampleDataMountPath + "/" + sampleDataSQLKey},
	}
	if needsOAuth {
		if err := r.ensureSampleDataOIDCSecret(ctx, dp, ns); err != nil {
			return false, err
		}
		env = append(env,
			corev1.EnvVar{Name: "TOKEN_URL", Value: oidc.tokenURL},
			corev1.EnvVar{Name: "OIDC_CLIENT_ID", Value: dataplatformv1alpha1.DefaultOIDCClientID},
			corev1.EnvVar{Name: "OIDC_USERNAME", Value: dataplatformv1alpha1.DefaultOIDCAdminUser},
			corev1.EnvVar{
				Name: "OIDC_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: secretSampleDataOIDC},
						Key:                  keyKeycloakAdminPassword,
					},
				},
			},
		)
	} else {
		env = append(env, corev1.EnvVar{Name: "TRINO_USER", Value: dataplatformv1alpha1.DefaultOIDCAdminUser})
	}

	labels := labelsFor(dp, componentSampleData)
	job = &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nameSampleDataJob,
			Namespace: ns,
			Labels:    labels,
			Annotations: map[string]string{
				sampleDataHashAnno: cmHash,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(6)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyOnFailure,
					SecurityContext: restrictedPodSecurity(uidSampleData, gidSampleData),
					Containers: []corev1.Container{{
						Name:            "seed",
						Image:           dp.Spec.SampleData.ImageOrDefault(),
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"python3", sampleDataMountPath + "/" + sampleDataScriptKey},
						Env:             env,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "sample",
							MountPath: sampleDataMountPath,
							ReadOnly:  true,
						}},
						SecurityContext: restrictedContainerSecurity(uidSampleData, gidSampleData),
					}},
					Volumes: []corev1.Volume{{
						Name: "sample",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapSampleData},
								DefaultMode:          ptr.To(int32(0444)),
							},
						},
					}},
				},
			},
		},
	}
	if err := r.apply(ctx, dp, job, func() error { return nil }); err != nil {
		return false, err
	}
	return false, nil
}

func (r *DataPlatformReconciler) ensureSampleDataOIDCSecret(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, trinoNS string) error {
	kcNS := dp.Spec.Auth.Keycloak.NamespaceOrDefault()
	password, err := r.getSecretData(ctx, secretKeycloakAdmin, kcNS, keyKeycloakAdminPassword)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretSampleDataOIDC, Namespace: trinoNS}}
	return r.apply(ctx, dp, secret, func() error {
		ensureLabels(secret, labelsFor(dp, componentSampleData))
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[keyKeycloakAdminPassword] = []byte(password)
		return nil
	})
}

func sampleDataJobHash(job *batchv1.Job) string {
	if job.Annotations == nil {
		return ""
	}
	return job.Annotations[sampleDataHashAnno]
}

func sampleDataSQL(catalog, schema string) string {
	fq := catalog + "." + schema
	var b strings.Builder
	b.WriteString("-- Generated by data-platform-operator. Demo Iceberg tables for local testing.\n")
	b.WriteString(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s\n", fq))
	b.WriteString(";\n")
	b.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.orders (\n", fq))
	b.WriteString("  order_id bigint,\n")
	b.WriteString("  customer varchar,\n")
	b.WriteString("  region varchar,\n")
	b.WriteString("  tenant_id bigint,\n")
	b.WriteString("  amount decimal(12,2),\n")
	b.WriteString("  ssn varchar,\n")
	b.WriteString("  email varchar\n")
	b.WriteString(")\n")
	b.WriteString(";\n")
	b.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s.invoices (\n", fq))
	b.WriteString("  invoice_id bigint,\n")
	b.WriteString("  order_id bigint,\n")
	b.WriteString("  region varchar,\n")
	b.WriteString("  email varchar,\n")
	b.WriteString("  total decimal(12,2)\n")
	b.WriteString(")\n")
	b.WriteString(";\n")
	// Seed only when empty so a Job retry after partial success stays idempotent.
	b.WriteString(fmt.Sprintf("INSERT INTO %s.orders (order_id, customer, region, tenant_id, amount, ssn, email)\n", fq))
	b.WriteString("SELECT * FROM (VALUES\n")
	b.WriteString("  (1, 'Acme Corp', 'emea', 100, CAST(120.50 AS decimal(12,2)), '111-22-3333', 'buyer@acme.example'),\n")
	b.WriteString("  (2, 'Globex', 'us-east', 200, CAST(89.00 AS decimal(12,2)), '222-33-4444', 'ops@globex.example'),\n")
	b.WriteString("  (3, 'Initech', 'emea', 100, CAST(450.25 AS decimal(12,2)), '333-44-5555', 'ap@initech.example'),\n")
	b.WriteString("  (4, 'Umbrella', 'us-west', 300, CAST(15.99 AS decimal(12,2)), '444-55-6666', 'purchasing@umbrella.example')\n")
	b.WriteString(") AS t(order_id, customer, region, tenant_id, amount, ssn, email)\n")
	b.WriteString(fmt.Sprintf("WHERE NOT EXISTS (SELECT 1 FROM %s.orders LIMIT 1)\n", fq))
	b.WriteString(";\n")
	b.WriteString(fmt.Sprintf("INSERT INTO %s.invoices (invoice_id, order_id, region, email, total)\n", fq))
	b.WriteString("SELECT * FROM (VALUES\n")
	b.WriteString("  (1001, 1, 'emea', 'buyer@acme.example', CAST(120.50 AS decimal(12,2))),\n")
	b.WriteString("  (1002, 2, 'us-east', 'ops@globex.example', CAST(89.00 AS decimal(12,2))),\n")
	b.WriteString("  (1003, 3, 'emea', 'ap@initech.example', CAST(450.25 AS decimal(12,2))),\n")
	b.WriteString("  (1004, 4, 'us-west', 'purchasing@umbrella.example', CAST(15.99 AS decimal(12,2)))\n")
	b.WriteString(") AS t(invoice_id, order_id, region, email, total)\n")
	b.WriteString(fmt.Sprintf("WHERE NOT EXISTS (SELECT 1 FROM %s.invoices LIMIT 1)\n", fq))
	b.WriteString(";\n")
	return b.String()
}

func sampleDataSeedScript() string {
	return `#!/usr/bin/env python3
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

def http_json(req):
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            body = resp.read().decode()
            if not body:
                return {}
            return json.loads(body)
    except urllib.error.HTTPError as e:
        detail = e.read().decode()
        raise SystemExit("HTTP %s for %s: %s" % (e.code, req.full_url, detail)) from e

def access_token():
    token_url = os.environ.get("TOKEN_URL", "")
    if not token_url:
        return None
    data = urllib.parse.urlencode({
        "grant_type": "password",
        "client_id": os.environ["OIDC_CLIENT_ID"],
        "username": os.environ["OIDC_USERNAME"],
        "password": os.environ["OIDC_PASSWORD"],
    }).encode()
    req = urllib.request.Request(token_url, data=data, method="POST")
    payload = http_json(req)
    token = payload.get("access_token")
    if not token:
        raise SystemExit("token response missing access_token: %s" % payload)
    return token

def jwt_sub(token):
    payload = token.split(".")[1]
    payload += "=" * (-len(payload) % 4)
    # Keycloak may use URL-safe base64; accept both.
    import base64
    raw = base64.urlsafe_b64decode(payload.encode())
    return json.loads(raw).get("sub") or json.loads(raw).get("preferred_username")

def run_statement(server, sql, headers):
    req = urllib.request.Request(
        server.rstrip("/") + "/v1/statement",
        data=sql.encode(),
        headers=dict(headers, **{"Content-Type": "text/plain"}),
        method="POST",
    )
    data = http_json(req)
    while True:
        err = data.get("error")
        if err:
            raise SystemExit("Trino error: %s" % err)
        state = (data.get("stats") or {}).get("state")
        if state in ("FINISHED", "FAILED"):
            if state == "FAILED":
                raise SystemExit("Trino query failed: %s" % data)
            return
        next_uri = data.get("nextUri")
        if not next_uri:
            return
        time.sleep(0.25)
        data = http_json(urllib.request.Request(next_uri, headers=headers))

def main():
    server = os.environ["TRINO_URL"]
    sql_path = os.environ["SQL_FILE"]
    headers = {"X-Trino-Source": "data-platform-sample-data"}
    token = access_token()
    if token:
        headers["Authorization"] = "Bearer " + token
        # Trino OAuth still requires X-Trino-User; it must match the token sub.
        headers["X-Trino-User"] = jwt_sub(token)
    else:
        headers["X-Trino-User"] = os.environ.get("TRINO_USER", "admin")

    raw = open(sql_path, encoding="utf-8").read()
    statements = []
    buf = []
    for line in raw.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("--"):
            continue
        buf.append(line)
        if stripped.endswith(";"):
            stmt = "\n".join(buf).rstrip().rstrip(";").strip()
            buf = []
            if stmt:
                statements.append(stmt)
    if buf:
        stmt = "\n".join(buf).strip()
        if stmt:
            statements.append(stmt)

    for i, stmt in enumerate(statements, 1):
        print("Running statement %s/%s..." % (i, len(statements)), flush=True)
        run_statement(server, stmt, headers)
    print("Sample data seed complete", flush=True)

if __name__ == "__main__":
    main()
`
}
