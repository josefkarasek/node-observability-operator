/*
Copyright 2022.

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

package nodeobservabilityruncontroller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/node-observability-operator/api/v1alpha1"
)

const (
	MCOKind       = "NodeObservabilityMachineConfig"
	MCOApiVersion = "nodeobservability.olm.openshift.io/v1alpha1"
	MCOName       = "nodeobservabilitymachineconfig-run"
)

// ensureMCO ensures that the node observability machineconfig is created
// Returns a Boolean value indicating whether it exists, a pointer to the
// daemonset and an error when relevant
func (r *NodeObservabilityRunReconciler) ensureMCO(ctx context.Context) (bool, *v1alpha1.NodeObservabilityMachineConfig, error) {
	desired := r.desiredMCO()
	nameSpace := types.NamespacedName{Namespace: "", Name: desired.Name}
	exist, current, err := r.currentMCO(ctx, nameSpace)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get NodeObservabilityMachineConfig: %v", err)
	}
	if !exist {
		if err := r.createMCO(ctx, desired); err != nil {
			return false, nil, err
		}
		return r.currentMCO(ctx, nameSpace)
	}
	return true, current, err
}

// currentMCO check if the node observability machineconfig exists
func (r *NodeObservabilityRunReconciler) currentMCO(ctx context.Context, nameSpace types.NamespacedName) (bool, *v1alpha1.NodeObservabilityMachineConfig, error) {
	mc := r.desiredMCO()
	if err := r.Get(ctx, nameSpace, mc); err != nil {
		if errors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, mc, nil
}

// createMachineConfigt creates the node observability machineconfig
func (r *NodeObservabilityRunReconciler) createMCO(ctx context.Context, mc *v1alpha1.NodeObservabilityMachineConfig) error {
	if err := r.Create(ctx, mc); err != nil {
		return fmt.Errorf("failed to create MachineConfig %s: %w", mc.Name, err)
	}
	r.Log.Info("created MachinConfig", "MachineConfig.Name", mc.Name)
	return nil
}

// desiredDaemonSet returns a DaemonSet object
func (r *NodeObservabilityRunReconciler) desiredMCO() *v1alpha1.NodeObservabilityMachineConfig {
	return &v1alpha1.NodeObservabilityMachineConfig{
		TypeMeta: metav1.TypeMeta{
			Kind:       MCOKind,
			APIVersion: MCOApiVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: MCOName,
		},
		Spec: v1alpha1.NodeObservabilityMachineConfigSpec{
			Debug: v1alpha1.NodeObservabilityDebug{
				EnableCrioProfiling: true,
			},
		},
	}
}
