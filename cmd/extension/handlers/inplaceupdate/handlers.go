/*
Copyright 2026 SUSE.

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

// Package inplaceupdate implements the CAPI in-place update hook handlers
// (CanUpdateMachine, CanUpdateMachineSet, UpdateMachine) for RKE2 clusters.
//
// CanUpdateMachine and CanUpdateMachineSet declare the set of RKE2ConfigSpec
// fields that can be applied without replacing the node. UpdateMachine drives
// the actual execution by writing a machine-plan Secret consumed by system-agent.
//
// Field allowlist rationale:
//   - In-place capable: changes that only require an RKE2/kubelet restart
//     (currently: Files only — see canUpdateRKE2ConfigSpec).
//   - Replace-required: changes that require full node re-provisioning
//     (PreRKE2Commands, PostRKE2Commands, PrivateRegistriesConfig, AirGapped,
//     AirGappedChecksum, CISProfile, and — until buildUpgradePlan supports
//     them — NodeLabels, NodeTaints, NodeNamePrefix, Kubelet).
package inplaceupdate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/olekukonko/errors"
	plan "github.com/rancher/rancher/pkg/plan"
	planv1alpha1 "github.com/rancher/rancher/pkg/plan/api/plan.cattle.io/v1alpha1"
	"gomodules.xyz/jsonpatch/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	bootstrapv1 "github.com/rancher/cluster-api-provider-rke2/bootstrap/api/v1beta2"
	controlplanev1 "github.com/rancher/cluster-api-provider-rke2/controlplane/api/v1beta2"
)

const (
	// secretTypeMachinePlan is the Secret.Type that Rancher's system-agent
	// sets on the machine-plan Secret it registers on first contact.
	secretTypeMachinePlan = "rke.cattle.io/machine-plan" //nolint:gosec // false positive: this is a constant, not a credential

	// inplaceUpdateStartedAnnotation records when this extension first
	// observed the current in-place update attempt for a Machine. Used to
	// bound how long DoUpdateMachine will keep retrying before giving up.
	inplaceUpdateStartedAnnotation = "rke2.cattle.io/inplace-update-started-at"

	// inplaceUpdateTimeout bounds total wait time across both "machine-plan
	// Secret not yet registered" and "plan in progress" retry loops. Past
	// this, DoUpdateMachine reports Failure so CAPRKE2 can fall back to a
	// rolling replace instead of polling forever.
	inplaceUpdateTimeout = 30 * time.Minute
)

// ErrMachinePlanSecretNotFound indicates system-agent has not yet
// registered the machine-plan Secret for this Machine. Callers should
// treat this as a transient, retryable state rather than a hard failure.
var ErrMachinePlanSecretNotFound = errors.New("machine-plan secret not found")

// ExtensionHandlers holds the shared client used by all in-place update hook handlers.
type ExtensionHandlers struct {
	decoder runtime.Decoder
	client  client.Client
}

// NewExtensionHandlers returns a new ExtensionHandlers for the in-place update hook handlers.
func NewExtensionHandlers(c client.Client) *ExtensionHandlers {
	scheme := runtime.NewScheme()
	_ = bootstrapv1.AddToScheme(scheme)
	_ = controlplanev1.AddToScheme(scheme)

	return &ExtensionHandlers{
		client: c,
		decoder: serializer.NewCodecFactory(scheme).UniversalDecoder(
			bootstrapv1.GroupVersion,
		),
	}
}

// canUpdateMachineSpec declares that this extension can update:
// * MachineSpec.Version.
// NOTE: consider other fields for in-place updates:
//   - MachineSpec.Version, MachineSpec.FailureDomain
func canUpdateMachineSpec(current, desired *clusterv1.MachineSpec) {
	if current.Version != desired.Version {
		current.Version = desired.Version
	}
}

// canUpdateRKE2ConfigSpec declares that this extension can update:
//   - RKE2ConfigSpec.Files
//
// NodeLabels, NodeTaints, Kubelet, and NodeNamePrefix are NOT included here
// even though they could theoretically change without a full re-provision,
// because buildUpgradePlan does not yet translate any of them into plan
// content. Declaring them here without implementing the corresponding
// instructions would cause CAPRKE2 to mark a Machine up-to-date after an
// update that silently did nothing for those fields. Add them back only
// alongside the matching buildUpgradePlan support.
// NodeNamePrefix is deliberately excluded permanently — renaming a running
// node's identity isn't a meaningful in-place operation regardless of
// buildUpgradePlan support.
func canUpdateRKE2ConfigSpec(current, desired *bootstrapv1.RKE2ConfigSpec) {
	if !reflect.DeepEqual(current.Files, desired.Files) {
		current.Files = desired.Files
	}
}

// DoCanUpdateMachine implements the CanUpdateMachine hook.
func (h *ExtensionHandlers) DoCanUpdateMachine(
	ctx context.Context,
	req *runtimehooksv1.CanUpdateMachineRequest,
	resp *runtimehooksv1.CanUpdateMachineResponse,
) {
	log := ctrl.LoggerFrom(ctx).WithValues("Machine", klog.KObj(&req.Desired.Machine))
	log.Info("CanUpdateMachine is called")

	currentMachine, desiredMachine,
		currentBootstrapConfig, desiredBootstrapConfig, err := h.getObjectsFromCanUpdateMachineRequest(req)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()

		return
	}

	// Declare changes that this Runtime Extension can update in-place.

	// Machine
	canUpdateMachineSpec(&currentMachine.Spec, &desiredMachine.Spec)

	// BootstrapConfig (RKE2Config)
	currentRKE2Config, isCurrentRKE2Config := currentBootstrapConfig.(*bootstrapv1.RKE2Config)
	desiredRKE2Config, isDesiredRKE2Config := desiredBootstrapConfig.(*bootstrapv1.RKE2Config)

	if isCurrentRKE2Config && isDesiredRKE2Config {
		canUpdateRKE2ConfigSpec(&currentRKE2Config.Spec, &desiredRKE2Config.Spec)
	}

	if err := h.computeCanUpdateMachineResponse(req, resp, currentMachine, currentBootstrapConfig); err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()

		return
	}

	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// DoCanUpdateMachineSet implements the CanUpdateMachineSet hook.
// Mirrors DoCanUpdateMachine but operates on MachineSet/RKE2ConfigTemplate.
func (h *ExtensionHandlers) DoCanUpdateMachineSet(
	ctx context.Context,
	req *runtimehooksv1.CanUpdateMachineSetRequest,
	resp *runtimehooksv1.CanUpdateMachineSetResponse,
) {
	log := ctrl.LoggerFrom(ctx).WithValues("MachineSet", klog.KObj(&req.Desired.MachineSet))
	log.Info("CanUpdateMachineSet is called")

	currentMachineSet, desiredMachineSet,
		currentBootstrapConfigTemplate, desiredBootstrapConfigTemplate, err := h.getObjectsFromCanUpdateMachineSetRequest(req)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()

		return
	}

	// Declare changes that this Runtime Extension can update in-place.

	// Machine
	canUpdateMachineSpec(&currentMachineSet.Spec.Template.Spec, &desiredMachineSet.Spec.Template.Spec)

	// BootstrapConfig (RKE2ConfigTemplate)
	currentRKE2ConfigTemplate, isCurrentRKE2ConfigTemplate := currentBootstrapConfigTemplate.(*bootstrapv1.RKE2ConfigTemplate)
	desiredRKE2ConfigTemplate, isDesiredRKE2ConfigTemplate := desiredBootstrapConfigTemplate.(*bootstrapv1.RKE2ConfigTemplate)

	if isCurrentRKE2ConfigTemplate && isDesiredRKE2ConfigTemplate {
		canUpdateRKE2ConfigSpec(&currentRKE2ConfigTemplate.Spec.Template.Spec, &desiredRKE2ConfigTemplate.Spec.Template.Spec)
	}

	if err := h.computeCanUpdateMachineSetResponse(req, resp, currentMachineSet, currentBootstrapConfigTemplate); err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = err.Error()

		return
	}

	resp.Status = runtimehooksv1.ResponseStatusSuccess
}

// DoUpdateMachine implements the UpdateMachine hook.
// It writes an upgrade plan to the machine-plan Secret consumed by Rancher's
// system-agent, then polls the Secret status on each subsequent call until the
// agent reports success or failure.
//
// Response contract (from UpdateMachineResponse.CommonRetryResponse):
//   - Status=Success + RetryAfterSeconds>0: update in progress, retry later
//   - Status=Success + RetryAfterSeconds=0: update complete
//   - Status=Failure: update failed; operator must intervene
func (h *ExtensionHandlers) DoUpdateMachine(
	ctx context.Context,
	req *runtimehooksv1.UpdateMachineRequest,
	resp *runtimehooksv1.UpdateMachineResponse,
) {
	machine := &req.Desired.Machine
	log := ctrl.LoggerFrom(ctx).WithValues("Machine", klog.KObj(machine))
	log.Info("UpdateMachine is called")

	defer func() {
		log.Info("UpdateMachine response",
			"status", resp.Status,
			"retryAfterSeconds", resp.RetryAfterSeconds,
		)
	}()

	desiredBootstrapConfig, _, err := h.decoder.Decode(
		req.Desired.BootstrapConfig.Raw, nil, req.Desired.BootstrapConfig.Object)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = fmt.Sprintf("failed to decode BootstrapConfig: %v", err)

		return
	}

	desiredRKE2Config, ok := desiredBootstrapConfig.(*bootstrapv1.RKE2Config)
	if !ok {
		// Not an RKE2Config (e.g. kubeadm bootstrap)
		resp.Status = runtimehooksv1.ResponseStatusSuccess

		return
	}

	// Bound total wait time so a Machine can't get stuck retrying forever if
	// the machine-plan Secret never appears or the plan never completes.
	timedOut, err := h.checkTimeout(ctx, machine)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = fmt.Sprintf("failed to check in-place update timeout: %v", err)

		return
	}

	if timedOut {
		log.Info("in-place update timed out; reporting failure so CAPRKE2 can fall back to a rolling replace")

		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = fmt.Sprintf("in-place update did not complete within %s", inplaceUpdateTimeout)

		if err := h.clearTimeoutAnnotation(ctx, machine); err != nil {
			log.Error(err, "failed to clear in-place update timeout annotation after timeout")
		}

		return
	}

	// Find the machine-plan Secret registered by system-agent on first contact.
	secret, err := h.findMachinePlanSecret(ctx, machine)
	if err != nil {
		if errors.Is(err, ErrMachinePlanSecretNotFound) {
			log.V(1).Info("machine-plan Secret not yet registered by system-agent; retrying")

			resp.Status = runtimehooksv1.ResponseStatusSuccess
			resp.RetryAfterSeconds = 30

			return
		}

		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = fmt.Sprintf("failed to find machine-plan Secret: %v", err)

		return
	}

	// Build the upgrade plan from the desired machine + config state.
	newPlan := buildUpgradePlan(machine, desiredRKE2Config)

	newPlanJSON, err := json.Marshal(newPlan)
	if err != nil {
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = fmt.Sprintf("failed to marshal upgrade plan: %v", err)

		return
	}

	// Idempotency: only write if the plan content changed.
	if !bytes.Equal(secret.Data["plan"], newPlanJSON) {
		if err := h.writePlanToSecret(ctx, secret, newPlanJSON); err != nil {
			resp.Status = runtimehooksv1.ResponseStatusFailure
			resp.Message = fmt.Sprintf("failed to write plan to Secret: %v", err)

			return
		}

		// Plan just written — wait for the agent to pick it up.
		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.RetryAfterSeconds = 30

		return
	}

	// Plan already written; inspect its execution status.
	ps := derivePlanStatus(secret, newPlanJSON)
	switch {
	case ps.Success():
		log.Info("Machine upgrade complete")

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.RetryAfterSeconds = 0

		if err := h.clearTimeoutAnnotation(ctx, machine); err != nil {
			log.Error(err, "failed to clear in-place update timeout annotation after success")
		}
	case ps.Failure():
		resp.Status = runtimehooksv1.ResponseStatusFailure
		resp.Message = "node-side plan execution failed; inspect the machine-plan Secret for details"

		if err := h.clearTimeoutAnnotation(ctx, machine); err != nil {
			log.Error(err, "failed to clear in-place update timeout annotation after failure")
		}
	default:
		log.V(1).Info("Machine upgrade in progress", "planStatus", ps.String())

		resp.Status = runtimehooksv1.ResponseStatusSuccess
		resp.RetryAfterSeconds = 30
	}
}

// checkTimeout stamps machine with inplaceUpdateStartedAnnotation on first
// call and returns true once inplaceUpdateTimeout has elapsed since. Callers
// should treat true as "give up and report Failure".
func (h *ExtensionHandlers) checkTimeout(ctx context.Context, machine *clusterv1.Machine) (bool, error) {
	startedAt, ok := machine.Annotations[inplaceUpdateStartedAnnotation]
	if !ok {
		patch := client.MergeFrom(machine.DeepCopy())
		if machine.Annotations == nil {
			machine.Annotations = map[string]string{}
		}

		machine.Annotations[inplaceUpdateStartedAnnotation] = time.Now().UTC().Format(time.RFC3339)

		return false, h.client.Patch(ctx, machine, patch)
	}

	started, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		// Malformed annotation shouldn't block retries; log it and treat as just-started.
		log := ctrl.LoggerFrom(ctx)
		log.Error(err, "Malformed inplace update started annotation, treating as just-started", "startedAt", startedAt)

		return false, nil
	}

	return time.Since(started) > inplaceUpdateTimeout, nil
}

// clearTimeoutAnnotation removes the started-at marker once an update
// reaches a terminal state (success or failure), so the next update attempt
// starts its timeout window fresh.
func (h *ExtensionHandlers) clearTimeoutAnnotation(ctx context.Context, machine *clusterv1.Machine) error {
	if _, ok := machine.Annotations[inplaceUpdateStartedAnnotation]; !ok {
		return nil
	}

	patch := client.MergeFrom(machine.DeepCopy())
	delete(machine.Annotations, inplaceUpdateStartedAnnotation)

	return h.client.Patch(ctx, machine, patch)
}

func (h *ExtensionHandlers) getObjectsFromCanUpdateMachineRequest(
	req *runtimehooksv1.CanUpdateMachineRequest,
) (*clusterv1.Machine, *clusterv1.Machine, runtime.Object, runtime.Object, error) {
	currentMachine := req.Current.Machine.DeepCopy()
	desiredMachine := req.Desired.Machine.DeepCopy()

	currentBootstrapConfig, _, err := h.decoder.Decode(req.Current.BootstrapConfig.Raw, nil, req.Current.BootstrapConfig.Object)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	desiredBootstrapConfig, _, err := h.decoder.Decode(req.Desired.BootstrapConfig.Raw, nil, req.Desired.BootstrapConfig.Object)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	return currentMachine, desiredMachine, currentBootstrapConfig, desiredBootstrapConfig, nil
}

// findMachinePlanSecret lists Secrets in the machine's namespace filtered by
// the six lifecycle labels (Cluster group+kind+name, Machine group+kind+name)
// that uniquely identify a machine-plan Secret, then returns the one with the
// system-agent Secret type.
// Returns (nil, nil) when the system-agent has not registered yet.
func (h *ExtensionHandlers) findMachinePlanSecret(
	ctx context.Context,
	machine *clusterv1.Machine,
) (*corev1.Secret, error) {
	clusterName := machine.Labels[clusterv1.ClusterNameLabel]

	secretList := &corev1.SecretList{}
	if err := h.client.List(ctx, secretList,
		client.InNamespace(machine.Namespace),
		client.MatchingLabels{
			planv1alpha1.ClusterLifecycleGroupLabel: clusterv1.GroupVersion.Group,
			planv1alpha1.ClusterLifecycleKindLabel:  "Cluster",
			planv1alpha1.ClusterLifecycleNameLabel:  clusterName,
			planv1alpha1.MachineLifecycleGroupLabel: clusterv1.GroupVersion.Group,
			planv1alpha1.MachineLifecycleKindLabel:  "Machine",
			planv1alpha1.MachineLifecycleNameLabel:  machine.Name,
		},
	); err != nil {
		return nil, err
	}

	for i := range secretList.Items {
		if secretList.Items[i].Type == secretTypeMachinePlan {
			return &secretList.Items[i], nil
		}
	}

	return nil, ErrMachinePlanSecretNotFound
}

// writePlanToSecret patches the machine-plan Secret with the new plan JSON and
// clears the old applied-plan marker so the agent treats this as a new task.
func (h *ExtensionHandlers) writePlanToSecret(
	ctx context.Context,
	secret *corev1.Secret,
	planJSON []byte,
) error {
	updated := secret.DeepCopy()

	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}

	updated.Data["plan"] = planJSON
	// Clear old applied-plan markers so system-agent picks up the new plan.
	delete(updated.Data, "appliedPlan")
	delete(updated.Data, plan.PlanStateKey)

	return h.client.Update(ctx, updated)
}

// buildUpgradePlan constructs a system-agent Plan that:
//  1. Writes all desired files from the RKE2Config to the node.
//  2. Installs the target RKE2 version via the standard install script.
//  3. Restarts the appropriate service unit (rke2-server or rke2-agent).
//
// Air-gapped upgrades are not yet supported — AirGapped is a replace-required
// field and won't change during an in-place update, so this path is only reached
// for non-air-gapped nodes.
func buildUpgradePlan(machine *clusterv1.Machine, rke2Config *bootstrapv1.RKE2Config) *plan.Plan {
	serviceUnit := rke2ServiceUnit(machine)
	version := machine.Spec.Version

	var planFiles []plan.File
	for _, f := range rke2Config.Spec.Files {
		planFiles = append(planFiles, plan.File{
			Path:        f.Path,
			Content:     encodeFileContent(f),
			Permissions: f.Permissions,
		})
	}

	return &plan.Plan{
		Files: planFiles,
		OneTimeInstructions: []plan.OneTimeInstruction{
			{
				CommonInstruction: plan.CommonInstruction{
					Name: "upgrade-rke2",
					// Script is written to disk and executed by the agent;
					// using a heredoc avoids shell-quoting issues with the
					// RKE2 version string (which contains '+').
					Script: fmt.Sprintf(
						"#!/bin/sh\nINSTALL_RKE2_VERSION='%s'\ncurl -sfL https://get.rke2.io | INSTALL_RKE2_VERSION=\"$INSTALL_RKE2_VERSION\" sh -",
						version,
					),
				},
			},
			{
				CommonInstruction: plan.CommonInstruction{
					Name:    "restart-rke2",
					Command: "systemctl",
					Args:    []string{"restart", serviceUnit},
				},
			},
		},
	}
}

// rke2ServiceUnit returns the systemd service unit for the machine's role.
func rke2ServiceUnit(machine *clusterv1.Machine) string {
	if _, isCP := machine.Labels[clusterv1.MachineControlPlaneLabel]; isCP {
		return "rke2-server"
	}

	return "rke2-agent"
}

// encodeFileContent returns the file content as a base64 string suitable for
// the system-agent plan.File.Content field, which always expects base64.
func encodeFileContent(f bootstrapv1.File) string {
	switch f.Encoding {
	case bootstrapv1.Base64, bootstrapv1.GzipBase64:
		return f.Content
	default:
		return base64.StdEncoding.EncodeToString([]byte(f.Content))
	}
}

// derivePlanStatus reads the execution state of the plan from the Secret's
// data fields. It handles both the new-style plan-state key written by recent
// system-agent versions and the old-style appliedPlan / failure-count fields.
func derivePlanStatus(secret *corev1.Secret, planJSON []byte) plan.PlanStatus {
	result := plan.PlanStatus{Secret: secret}

	// New-style: plan-state key (system-agent >= v0.3).
	if state := plan.PlanState(secret.Data[plan.PlanStateKey]); state != "" {
		switch state {
		case plan.PlanStateSucceeded:
			result.Applied = true
			result.ProbesPassed = true
		case plan.PlanStateFailed:
			result.Failed = true
		case plan.PlanStateCancelled:
			// A cancelled plan is treated as a failure today: CAPRKE2 will
			// fall back to a rolling replace per the proposal's "fallback to
			// immutable rollouts" tenet. If cancellation should instead
			// resume with a freshly-written plan (cancel-then-rewrite, per
			// the system-agent RFD's orchestrator protocol), this branch
			// should fall through to writing a new plan instead —
			// deliberately left as Failed until someone decides which
			// behavior is wanted.
			result.Failed = true
		case plan.PlanStateInProgress:
			result.InProgress = true
		default: // PlanStatePending or unknown
			result.Pending = true
		}

		return result
	}

	// Old-style: compare plan bytes to appliedPlan field.
	if appliedPlan := secret.Data["appliedPlan"]; len(appliedPlan) > 0 {
		if bytes.Equal(planJSON, appliedPlan) {
			result.Applied = true
			result.ProbesPassed = true

			return result
		}
	}

	// Old-style failure detection: failed-checksum + failure-count.
	if failedChecksum := string(secret.Data["failed-checksum"]); failedChecksum != "" {
		if failedChecksum == plan.PlanHash(planJSON) {
			if fc, err := strconv.Atoi(string(secret.Data["failure-count"])); err == nil && fc > 0 {
				rawThreshold := secret.Data["failure-threshold"]
				if len(rawThreshold) > 0 {
					if ft, err := strconv.Atoi(string(rawThreshold)); err == nil && (ft == -1 || fc < ft) {
						result.Failing = true

						return result
					}
				}

				result.Failed = true

				return result
			}
		}
	}

	// Plan is written but the agent hasn't applied it yet.
	result.InProgress = true

	return result
}

//nolint:dupl // mirrors decodeCanUpdateMachineRequest by design: same shape, different request type.
func (h *ExtensionHandlers) getObjectsFromCanUpdateMachineSetRequest(
	req *runtimehooksv1.CanUpdateMachineSetRequest,
) (*clusterv1.MachineSet, *clusterv1.MachineSet, runtime.Object, runtime.Object, error) {
	currentMachineSet := req.Current.MachineSet.DeepCopy()
	desiredMachineSet := req.Desired.MachineSet.DeepCopy()

	currentBootstrapConfigTemplate, _, err := h.decoder.Decode(
		req.Current.BootstrapConfigTemplate.Raw, nil, req.Current.BootstrapConfigTemplate.Object)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	desiredBootstrapConfigTemplate, _, err := h.decoder.Decode(
		req.Desired.BootstrapConfigTemplate.Raw, nil, req.Desired.BootstrapConfigTemplate.Object)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	return currentMachineSet, desiredMachineSet, currentBootstrapConfigTemplate, desiredBootstrapConfigTemplate, nil
}

func (h *ExtensionHandlers) computeCanUpdateMachineResponse(
	req *runtimehooksv1.CanUpdateMachineRequest,
	resp *runtimehooksv1.CanUpdateMachineResponse,
	currentMachine *clusterv1.Machine,
	currentBootstrapConfig runtime.Object,
) error {
	marshalledCurrentMachine, err := json.Marshal(req.Current.Machine)
	if err != nil {
		return err
	}

	machinePatch, err := createJSONPatch(marshalledCurrentMachine, currentMachine)
	if err != nil {
		return err
	}

	bootstrapConfigPatch, err := createJSONPatch(req.Current.BootstrapConfig.Raw, currentBootstrapConfig)
	if err != nil {
		return err
	}

	resp.MachinePatch = runtimehooksv1.Patch{
		PatchType: runtimehooksv1.JSONPatchType,
		Patch:     machinePatch,
	}
	resp.BootstrapConfigPatch = runtimehooksv1.Patch{
		PatchType: runtimehooksv1.JSONPatchType,
		Patch:     bootstrapConfigPatch,
	}

	return nil
}

//nolint:dupl // mirrors computeCanUpdateMachineResponse by design: same shape, different request type.
func (h *ExtensionHandlers) computeCanUpdateMachineSetResponse(
	req *runtimehooksv1.CanUpdateMachineSetRequest,
	resp *runtimehooksv1.CanUpdateMachineSetResponse,
	currentMachineSet *clusterv1.MachineSet,
	currentBootstrapConfigTemplate runtime.Object,
) error {
	marshalledCurrentMachineSet, err := json.Marshal(req.Current.MachineSet)
	if err != nil {
		return err
	}

	machineSetPatch, err := createJSONPatch(marshalledCurrentMachineSet, currentMachineSet)
	if err != nil {
		return err
	}

	bootstrapConfigTemplatePatch, err := createJSONPatch(
		req.Current.BootstrapConfigTemplate.Raw, currentBootstrapConfigTemplate)
	if err != nil {
		return err
	}

	resp.MachineSetPatch = runtimehooksv1.Patch{
		PatchType: runtimehooksv1.JSONPatchType,
		Patch:     machineSetPatch,
	}

	resp.BootstrapConfigTemplatePatch = runtimehooksv1.Patch{
		PatchType: runtimehooksv1.JSONPatchType,
		Patch:     bootstrapConfigTemplatePatch,
	}

	return nil
}

// createJSONPatch creates a RFC 6902 JSON patch from the original and the modified object.
func createJSONPatch(marshalledOriginal []byte, modified runtime.Object) ([]byte, error) {
	marshalledModified, err := json.Marshal(modified)
	if err != nil {
		return nil, errors.Errorf("failed to marshal modified object: %v", err)
	}

	patch, err := jsonpatch.CreatePatch(marshalledOriginal, marshalledModified)
	if err != nil {
		return nil, errors.Errorf("failed to create patch: %v", err)
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, errors.Errorf("failed to marshal patch: %v", err)
	}

	return patchBytes, nil
}
