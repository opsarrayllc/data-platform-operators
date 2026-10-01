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
	"embed"
	"io"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

//go:embed embed/crds.yaml
var argoCRDs embed.FS

func (r *OrchestrationReconciler) applyArgoCRDs(ctx context.Context, o *orchestrationv1alpha1.Orchestration) error {
	raw, err := argoCRDs.ReadFile("embed/crds.yaml")
	if err != nil {
		return err
	}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := dec.Decode(crd); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if crd.Name == "" {
			continue
		}
		if err := r.applyCRD(ctx, o, crd); err != nil {
			return err
		}
	}
}

func (r *OrchestrationReconciler) applyCRD(ctx context.Context, o *orchestrationv1alpha1.Orchestration, desired *apiextensionsv1.CustomResourceDefinition) error {
	existing := &apiextensionsv1.CustomResourceDefinition{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		desired.ResourceVersion = ""
		desired.Status = apiextensionsv1.CustomResourceDefinitionStatus{}
		if err := controllerutil.SetControllerReference(o, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return nil
	}
	existing.Spec = desired.Spec
	return r.Update(ctx, existing)
}
