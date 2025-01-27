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

package machineconfigcontroller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/go-logr/logr"

	"github.com/openshift/node-observability-operator/api/v1alpha1"
)

//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=nodeobservabilitymachineconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=nodeobservabilitymachineconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=nodeobservability.olm.openshift.io,resources=nodeobservabilitymachineconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigs,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=machineconfiguration.openshift.io,resources=machineconfigpools,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
//
// Reconcile here is for NodeObservabilityMachineConfig controller, which aims
// to keep the state as required by the NodeObservability operator. If for any
// service(ex: CRI-O) requires debugging to be enabled/disabled through the
// MachineConfigs, controller creates the required MachineConfigs, MachineConfigPool
// and labels the nodes where the changes are to be applied.
func (r *MachineConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	var err error
	if r.Log, err = logr.FromContext(ctx); err != nil {
		return ctrl.Result{}, err
	}

	r.Log.V(3).Info("Reconciling MachineConfig of Nodeobservability operator")

	// Fetch the nodeobservability.olm.openshift.io/machineconfig CR
	r.CtrlConfig = &v1alpha1.NodeObservabilityMachineConfig{}
	if err = r.Get(ctx, req.NamespacedName, r.CtrlConfig); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			r.Log.Info("MachineConfig resource not found. Ignoring could have been deleted", "name", req.NamespacedName.Name)
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		r.Log.Error(err, "failed to fetch MachineConfig")
		return ctrl.Result{RequeueAfter: defaultRequeueTime}, err
	}
	r.Log.V(3).Info("MachineConfig resource found")

	if !r.CtrlConfig.DeletionTimestamp.IsZero() {
		r.Log.Info("MachineConfig resource marked for deletetion, cleaning up")
		return r.cleanUp(ctx, req)
	}

	// Set finalizers on the NodeObservability/MachineConfig resource
	updated, err := r.withFinalizers(ctx, req)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update MachineConfig with finalizers: %w", err)
	}
	updated.DeepCopyInto(r.CtrlConfig)

	if err := r.inspectProfilingMCReq(ctx); err != nil {
		r.Log.Error(err, "failed to reconcile requested configuration")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, err
	}

	defer func() {
		errUpdate := r.Status().Update(ctx, r.CtrlConfig)
		if errUpdate != nil {
			r.Log.Error(errUpdate, "failed to update status")
			err = utilerrors.NewAggregate([]error{err, errUpdate})
		}
	}()

	res, err := r.monitorProgress(ctx, req)
	if err != nil {
		r.Log.Error(err, "failed to fetch MachineConfigPool status")
	}

	return res, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *MachineConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NodeObservabilityMachineConfig{}).
		Complete(r)
}

func (r *MachineConfigReconciler) cleanUp(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if hasFinalizer(r.CtrlConfig) {
		// Remove the finalizer.
		if _, err := r.withoutFinalizers(ctx, req, finalizer); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from MachineConfig %s: %w", r.CtrlConfig.Name, err)
		}
	}
	return ctrl.Result{}, nil
}

func (r *MachineConfigReconciler) withFinalizers(ctx context.Context, req ctrl.Request) (*v1alpha1.NodeObservabilityMachineConfig, error) {
	withFinalizers := &v1alpha1.NodeObservabilityMachineConfig{}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, req.NamespacedName, withFinalizers); err != nil {
			r.Log.Error(err, "failed to fetch nodeobservabilitymachineconfig resource for updating finalizer")
			return err
		}

		if hasFinalizer(withFinalizers) {
			return nil
		}
		withFinalizers.Finalizers = append(withFinalizers.Finalizers, finalizer)

		if err := r.Update(ctx, withFinalizers); err != nil {
			r.Log.Error(err, "failed to update nodeobservabilitymachineconfig resource finalizers")
			return err
		}

		return nil
	})

	return withFinalizers, err
}

func (r *MachineConfigReconciler) withoutFinalizers(ctx context.Context, req ctrl.Request, finalizer string) (*v1alpha1.NodeObservabilityMachineConfig, error) {
	withoutFinalizers := &v1alpha1.NodeObservabilityMachineConfig{}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := r.Get(ctx, req.NamespacedName, withoutFinalizers); err != nil {
			r.Log.Error(err, "failed to fetch nodeobservabilitymachineconfig resource for removing finalizer")
			return err
		}

		if !hasFinalizer(withoutFinalizers) {
			return nil
		}

		newFinalizers := make([]string, 0)
		for _, item := range withoutFinalizers.Finalizers {
			if item == finalizer {
				continue
			}
			newFinalizers = append(newFinalizers, item)
		}

		if len(newFinalizers) == 0 {
			// Sanitize for unit tests, so we don't need to distinguish empty array
			// and nil.
			newFinalizers = nil
		}

		withoutFinalizers.Finalizers = newFinalizers
		if err := r.Update(ctx, withoutFinalizers); err != nil {
			r.Log.Error(err, "failed to remove nodeobservabilitymachineconfig resource finalizers")
			return err
		}

		return nil
	})

	return withoutFinalizers, err
}

func hasFinalizer(mc *v1alpha1.NodeObservabilityMachineConfig) bool {
	hasFinalizer := false
	for _, f := range mc.Finalizers {
		if f == finalizer {
			hasFinalizer = true
			break
		}
	}
	return hasFinalizer
}

// inspectProfilingMCReq is for checking and creating required configs
// if debugging is enabled
func (r *MachineConfigReconciler) inspectProfilingMCReq(ctx context.Context) error {
	if r.CtrlConfig.Status.IsMachineConfigInProgress() {
		r.Log.Info("previous reconcile initiated operation in progress, changes not applied")
		return nil
	}

	if r.CtrlConfig.Spec.Debug.EnableCrioProfiling {
		return r.ensureProfConfEnabled(ctx)
	} else {
		return r.ensureProfConfDisabled(ctx)
	}
}

// ensureProfConfEnabled is for enabling the profiling of requested services
func (r *MachineConfigReconciler) ensureProfConfEnabled(ctx context.Context) (err error) {

	var modCount, setEnabledCondition int
	if modCount, err = r.ensureReqNodeLabelExists(ctx); err != nil {
		r.Log.Error(err, "failed to ensure nodes are labelled")
		// fails for even one node revert changes made
		return r.revertNodeLabeling(ctx)
	}
	setEnabledCondition += modCount
	if modCount, err = r.ensureReqMCPExists(ctx); err != nil {
		r.Log.Error(err, "failed to ensure mcp exists")
		return
	}
	setEnabledCondition += modCount
	if modCount, err = r.ensureReqMCExists(ctx); err != nil {
		r.Log.Error(err, "failed to ensure mc exists")
		return
	}
	setEnabledCondition += modCount

	if setEnabledCondition > 0 {
		// FIXME: missing msg
		r.CtrlConfig.Status.SetCondition(v1alpha1.DebugEnabled, metav1.ConditionTrue, v1alpha1.ReasonEnabled, "")
		r.CtrlConfig.Status.SetCondition(v1alpha1.DebugReady, metav1.ConditionFalse, v1alpha1.ReasonInProgress, "")
	}

	return
}

// ensureProfConfDisabled is for disabling the profiling of requested services
func (r *MachineConfigReconciler) ensureProfConfDisabled(ctx context.Context) (err error) {

	modCount := 0
	if modCount, err = r.ensureReqNodeLabelNotExists(ctx); err != nil {
		r.Log.Error(err, "failed to ensure nodes are not labelled")
		// fails for even one node revert changes made
		return r.revertNodeUnlabeling(ctx)
	}

	// FIXME: shouldn't these conditions be always set to False, when ensureProfConfDisabled() is called?
	if modCount > 0 {
		// FIXME: missing msg
		r.CtrlConfig.Status.SetCondition(v1alpha1.DebugEnabled, metav1.ConditionFalse, v1alpha1.ReasonDisabled, "")
		r.CtrlConfig.Status.SetCondition(v1alpha1.DebugReady, metav1.ConditionFalse, v1alpha1.ReasonDisabled, "")
	}

	return nil
}

// ensureReqMCExists is for ensuring the required machine config exists
func (r *MachineConfigReconciler) ensureReqMCExists(ctx context.Context) (int, error) {
	updatedCount := 0
	if r.CtrlConfig.Spec.Debug.EnableCrioProfiling {
		updated, err := r.enableCrioProf(ctx)
		if err != nil {
			return updatedCount, err
		}
		if updated {
			updatedCount++
		}
	}
	return updatedCount, nil
}

// ensureReqMCNotExists is for ensuring the machine config created when
// profiling was enabled is indeed removed
func (r *MachineConfigReconciler) ensureReqMCNotExists(ctx context.Context) error {
	if !r.CtrlConfig.Spec.Debug.EnableCrioProfiling {
		return r.disableCrioProf(ctx)
	}
	return nil
}

// ensureReqMCPExists is for ensuring the required machine config pool exists
func (r *MachineConfigReconciler) ensureReqMCPExists(ctx context.Context) (int, error) {
	updatedCount := 0
	updated, err := r.createProfMCP(ctx)
	if err != nil {
		return updatedCount, err
	}
	if updated {
		updatedCount++
	}
	return updatedCount, nil
}

// ensureReqMCPNotExists is for ensuring the machine config pool created when
// profiling was enabled is indeed removed
func (r *MachineConfigReconciler) ensureReqMCPNotExists(ctx context.Context) error {
	return r.deleteProfMCP(ctx)
}

func (r *MachineConfigReconciler) monitorProgress(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {

	if r.CtrlConfig.Status.IsDebuggingEnabled() {
		if result, err = r.CheckNodeObservabilityMCPStatus(ctx); err != nil {
			return
		}
	}

	if !r.CtrlConfig.Status.IsDebuggingEnabled() || r.CtrlConfig.Status.IsDebuggingFailed() {
		if result, err = r.checkWorkerMCPStatus(ctx); err != nil {
			return
		}
	}

	return
}

// revertEnabledProfConf is for restoring the cluster state to
// as it was, before enabling the debug configurations
func (r *MachineConfigReconciler) revertEnabledProfConf(ctx context.Context) error {
	_, err := r.ensureReqNodeLabelNotExists(ctx)
	return err
}
