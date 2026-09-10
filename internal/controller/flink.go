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
	"maps"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

func (r *DataPlatformReconciler) reconcileFlinkStack(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, _ oidcConfig) (bool, error) {
	if !dp.Spec.Flink.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionTrue, reasonDisabled, "Flink is disabled")
		return false, nil
	}

	ns := dp.Spec.Flink.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, dp, ns, componentFlinkJobManager); err != nil {
		return false, err
	}
	if err := r.reconcileFlink(ctx, dp); err != nil {
		return false, err
	}
	return !conditionTrue(dp, dataplatformv1alpha1.ConditionFlinkReady), nil
}

func (r *DataPlatformReconciler) reconcileFlink(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform) error {
	ns := dp.Spec.Flink.NamespaceOrDefault()
	dp.Status.FlinkEndpoint = clusterServiceURL(nameFlinkJobManager, ns, flinkRESTPort)

	conf := flinkConfiguration(dp)
	cfgHash := hashData(conf)

	if err := r.applyFlinkConfigMap(ctx, dp, ns, conf); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applyFlinkJobManagerService(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applyFlinkUIService(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applyFlinkJobManager(ctx, dp, ns, cfgHash); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.reconcileFlinkTaskManagers(ctx, dp, ns, cfgHash); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}

	jmReady, err := r.deploymentReady(ctx, ns, nameFlinkJobManager)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if !jmReady {
		setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonNotReady, "Flink JobManager is not ready")
		return nil
	}

	if dp.Spec.Flink.TaskManagersOrDefault() > 0 {
		tmReady, err := r.deploymentReady(ctx, ns, nameFlinkTaskManager)
		if err != nil {
			setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonError, err.Error())
			return err
		}
		if !tmReady {
			setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionFalse, reasonNotReady, "Flink TaskManagers are not ready")
			return nil
		}
	}

	setCondition(dp, dataplatformv1alpha1.ConditionFlinkReady, metav1.ConditionTrue, reasonReady, "Flink session cluster is ready")
	return nil
}

func flinkConfiguration(dp *dataplatformv1alpha1.DataPlatform) string {
	ns := dp.Spec.Flink.NamespaceOrDefault()
	props := map[string]string{
		"jobmanager.rpc.address":           fmt.Sprintf("%s.%s.svc", nameFlinkJobManager, ns),
		"jobmanager.rpc.port":              strconv.Itoa(int(flinkRPCPort)),
		"jobmanager.bind-host":             "0.0.0.0",
		"jobmanager.memory.process.size":   dp.Spec.Flink.JobManager.ProcessMemoryOrDefault(),
		"taskmanager.bind-host":            "0.0.0.0",
		"taskmanager.rpc.port":             strconv.Itoa(int(flinkTMRpcPort)),
		"taskmanager.data.port":            strconv.Itoa(int(flinkDataPort)),
		"taskmanager.memory.process.size":  dp.Spec.Flink.TaskManager.ProcessMemoryOrDefault(),
		"taskmanager.numberOfTaskSlots":    strconv.Itoa(int(dp.Spec.Flink.TaskSlotsOrDefault())),
		"rest.bind-address":                "0.0.0.0",
		"rest.port":                        strconv.Itoa(int(flinkRESTPort)),
		"blob.server.port":                 strconv.Itoa(int(flinkBlobPort)),
		"query.server.port":                strconv.Itoa(int(flinkQueryPort)),
		"parallelism.default":              "1",
		"execution.checkpointing.interval": "60s",
	}
	maps.Copy(props, dp.Spec.Flink.ExtraConfig)
	return renderFlinkConf(props)
}

func renderFlinkConf(props map[string]string) string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(props[k])
		b.WriteString("\n")
	}
	return b.String()
}

func (r *DataPlatformReconciler) applyFlinkConfigMap(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, conf string,
) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapFlink, Namespace: ns}}
	labels := labelsFor(dp, componentFlinkJobManager)
	return r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labels)
		cm.Data = map[string]string{"flink-conf.yaml": conf}
		return nil
	})
}

func (r *DataPlatformReconciler) applyFlinkJobManagerService(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameFlinkJobManager, Namespace: ns}}
	labels := labelsFor(dp, componentFlinkJobManager)
	return r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Type = dp.Spec.Flink.Service.TypeOrDefault()
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "rpc", Port: flinkRPCPort, TargetPort: intstr.FromInt32(flinkRPCPort)},
			{Name: "blob", Port: flinkBlobPort, TargetPort: intstr.FromInt32(flinkBlobPort)},
			{Name: portNameHTTP, Port: flinkRESTPort, TargetPort: intstr.FromInt32(flinkRESTPort)},
		}
		return nil
	})
}

func (r *DataPlatformReconciler) applyFlinkUIService(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameFlink, Namespace: ns}}
	labels := labelsFor(dp, componentFlinkJobManager)
	return r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       portNameHTTP,
			Port:       flinkRESTPort,
			TargetPort: intstr.FromInt32(flinkRESTPort),
		}}
		return nil
	})
}

func (r *DataPlatformReconciler) applyFlinkJobManager(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, cfgHash string,
) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameFlinkJobManager, Namespace: ns}}
	labels := labelsFor(dp, componentFlinkJobManager)
	return r.apply(ctx, dp, deploy, func() error {
		ensureLabels(deploy, labels)
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		deploy.Spec.Replicas = ptr.To(int32(1))
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Labels = labels
		deploy.Spec.Template.Spec = flinkPodSpec(
			dp.Spec.Flink.ImageOrDefault(),
			"jobmanager",
			dp.Spec.Flink.JobManager.Resources,
			dp.Spec.Flink.ExtraEnv,
			[]corev1.ContainerPort{
				{Name: "rpc", ContainerPort: flinkRPCPort},
				{Name: "blob", ContainerPort: flinkBlobPort},
				{Name: portNameHTTP, ContainerPort: flinkRESTPort},
			},
			true,
		)
		return nil
	})
}

func (r *DataPlatformReconciler) reconcileFlinkTaskManagers(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, cfgHash string,
) error {
	replicas := dp.Spec.Flink.TaskManagersOrDefault()
	if replicas == 0 {
		deploy := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{Name: nameFlinkTaskManager, Namespace: ns}, deploy)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		logf.FromContext(ctx).Info("Deleting Flink TaskManager Deployment", "name", nameFlinkTaskManager)
		return client.IgnoreNotFound(r.Delete(ctx, deploy))
	}

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameFlinkTaskManager, Namespace: ns}}
	labels := labelsFor(dp, componentFlinkTaskManager)
	return r.apply(ctx, dp, deploy, func() error {
		ensureLabels(deploy, labels)
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		deploy.Spec.Replicas = ptr.To(replicas)
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Labels = labels
		deploy.Spec.Template.Spec = flinkPodSpec(
			dp.Spec.Flink.ImageOrDefault(),
			"taskmanager",
			dp.Spec.Flink.TaskManager.Resources,
			dp.Spec.Flink.ExtraEnv,
			[]corev1.ContainerPort{
				{Name: "data", ContainerPort: flinkDataPort},
				{Name: "rpc", ContainerPort: flinkTMRpcPort},
				{Name: "query", ContainerPort: flinkQueryPort},
			},
			false,
		)
		return nil
	})
}

func flinkPodSpec(
	image, role string,
	resources corev1.ResourceRequirements,
	env []corev1.EnvVar,
	ports []corev1.ContainerPort,
	restProbe bool,
) corev1.PodSpec {
	// JobManager exposes REST on 8081. TaskManagers listen on a pinned RPC
	// port (not the JobManager's 6123); probing 6123 always fails.
	probePort := flinkTMRpcPort
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(probePort)},
		},
		PeriodSeconds: 10,
	}
	if restProbe {
		probe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/config",
					Port: intstr.FromInt32(flinkRESTPort),
				},
			},
			PeriodSeconds: 10,
		}
	}
	containerEnv := append([]corev1.EnvVar{{
		Name: "FLINK_PROPERTIES",
		ValueFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapFlink},
				Key:                  "flink-conf.yaml",
			},
		},
	}}, env...)
	return corev1.PodSpec{
		SecurityContext: restrictedPodSecurity(uidFlink, gidFlink),
		Containers: []corev1.Container{{
			Name:            nameFlink,
			Image:           image,
			Args:            []string{role},
			Ports:           ports,
			Env:             containerEnv,
			ReadinessProbe:  probe,
			Resources:       resources,
			SecurityContext: restrictedContainerSecurity(uidFlink, gidFlink),
		}},
	}
}
