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

package inplaceupdate

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	plan "github.com/rancher/rancher/pkg/plan"
	planv1alpha1 "github.com/rancher/rancher/pkg/plan/api/plan.cattle.io/v1alpha1"

	bootstrapv1 "github.com/rancher/cluster-api-provider-rke2/bootstrap/api/v1beta2"
)

// helpers

func newHandlers(objects ...client.Object) *ExtensionHandlers {
	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		Build()
	return NewExtensionHandlers(c)
}

func mustRaw(obj runtime.Object) runtime.RawExtension {
	raw, err := json.Marshal(obj)
	Expect(err).NotTo(HaveOccurred())
	return runtime.RawExtension{Raw: raw}
}

func baseMachine(version string) clusterv1.Machine {
	return clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "m1"},
		Spec: clusterv1.MachineSpec{
			ClusterName: "test",
			Version:     version,
		},
	}
}

func cpMachine(version string) clusterv1.Machine {
	m := baseMachine(version)
	m.Labels = map[string]string{clusterv1.MachineControlPlaneLabel: ""}
	return m
}

func baseRKE2Config(mutate func(*bootstrapv1.RKE2ConfigSpec)) *bootstrapv1.RKE2Config {
	cfg := &bootstrapv1.RKE2Config{
		TypeMeta: metav1.TypeMeta{
			APIVersion: bootstrapv1.GroupVersion.String(),
			Kind:       "RKE2Config",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "m1"},
	}
	if mutate != nil {
		mutate(&cfg.Spec)
	}
	return cfg
}

func baseMachineSet(version string) clusterv1.MachineSet {
	return clusterv1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ms1"},
		Spec: clusterv1.MachineSetSpec{
			ClusterName: "test",
			Template: clusterv1.MachineTemplateSpec{
				Spec: clusterv1.MachineSpec{
					ClusterName: "test",
					Version:     version,
				},
			},
		},
	}
}

func baseRKE2ConfigTemplate(mutate func(*bootstrapv1.RKE2ConfigSpec)) *bootstrapv1.RKE2ConfigTemplate {
	tpl := &bootstrapv1.RKE2ConfigTemplate{
		TypeMeta: metav1.TypeMeta{
			APIVersion: bootstrapv1.GroupVersion.String(),
			Kind:       "RKE2ConfigTemplate",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "ms1"},
	}
	if mutate != nil {
		mutate(&tpl.Spec.Template.Spec)
	}
	return tpl
}

// machinePlanSecret creates a Secret with the correct labels and type to be
// found by findMachinePlanSecret for the m1 machine in namespace "default".
func machinePlanSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine-plan-m1",
			Namespace: "default",
			Labels: map[string]string{
				planv1alpha1.ClusterLifecycleGroupLabel: "cluster.x-k8s.io",
				planv1alpha1.ClusterLifecycleKindLabel:  "Cluster",
				planv1alpha1.ClusterLifecycleNameLabel:  "test",
				planv1alpha1.MachineLifecycleNameLabel:  "m1",
			},
		},
		Type: secretTypeMachinePlan,
		Data: data,
	}
}

func canUpdateMachineReq(
	currentMachine, desiredMachine clusterv1.Machine,
	currentBC, desiredBC runtime.Object,
) *runtimehooksv1.CanUpdateMachineRequest {
	return &runtimehooksv1.CanUpdateMachineRequest{
		Current: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:         currentMachine,
			BootstrapConfig: mustRaw(currentBC),
		},
		Desired: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:         desiredMachine,
			BootstrapConfig: mustRaw(desiredBC),
		},
	}
}

func canUpdateMachineSetReq(
	currentMS, desiredMS clusterv1.MachineSet,
	currentBCT, desiredBCT runtime.Object,
) *runtimehooksv1.CanUpdateMachineSetRequest {
	return &runtimehooksv1.CanUpdateMachineSetRequest{
		Current: runtimehooksv1.CanUpdateMachineSetRequestObjects{
			MachineSet:              currentMS,
			BootstrapConfigTemplate: mustRaw(currentBCT),
		},
		Desired: runtimehooksv1.CanUpdateMachineSetRequestObjects{
			MachineSet:              desiredMS,
			BootstrapConfigTemplate: mustRaw(desiredBCT),
		},
	}
}

func updateMachineReq(machine clusterv1.Machine, rke2Config *bootstrapv1.RKE2Config) *runtimehooksv1.UpdateMachineRequest {
	return &runtimehooksv1.UpdateMachineRequest{
		Desired: runtimehooksv1.UpdateMachineRequestObjects{
			Machine:         machine,
			BootstrapConfig: mustRaw(rke2Config),
		},
	}
}

// tests

var _ = Describe("DoCanUpdateMachine", func() {
	It("absorbs version changes", func() {
		h := newHandlers()
		req := canUpdateMachineReq(
			baseMachine("v1.33.0"), baseMachine("v1.33.1"),
			baseRKE2Config(nil), baseRKE2Config(nil),
		)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.MachinePatch.PatchType).To(Equal(runtimehooksv1.JSONPatchType))
		Expect(string(resp.MachinePatch.Patch)).To(ContainSubstring("v1.33.1"))
		Expect(resp.InfrastructureMachinePatch.IsDefined()).To(BeFalse())
	})

	It("absorbs Files changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.Files = []bootstrapv1.File{{Path: "/etc/test", Content: "hello"}}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.BootstrapConfigPatch.PatchType).To(Equal(runtimehooksv1.JSONPatchType))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("/etc/test"))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring(`"content":"hello"`))
	})

	It("absorbs NodeLabels changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.NodeLabels = []string{"env=prod"}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("env=prod"))
	})

	It("absorbs NodeTaints changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.NodeTaints = []string{"dedicated=gpu:NoSchedule"}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("dedicated=gpu:NoSchedule"))
	})

	It("absorbs NodeNamePrefix changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.NodeNamePrefix = "worker-"
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("worker-"))
	})

	It("absorbs Kubelet config changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.Kubelet = &bootstrapv1.ComponentConfig{
				ExtraArgs: []string{"--max-pods=200"},
			}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("--max-pods=200"))
	})

	It("absorbs AdditionalUserData changes", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.AdditionalUserData = bootstrapv1.AdditionalUserData{
				Config: "users:\n  - name: ops\n",
			}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(ContainSubstring("ops"))
	})

	It("does not absorb PreRKE2Commands changes (replace-required)", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.PreRKE2Commands = []string{"echo pre"}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).NotTo(ContainSubstring("preRKE2Commands"))
	})

	It("does not absorb PostRKE2Commands changes (replace-required)", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.PostRKE2Commands = []string{"echo post"}
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).NotTo(ContainSubstring("postRKE2Commands"))
	})

	It("does not absorb AirGapped changes (replace-required)", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.AirGapped = true
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).NotTo(ContainSubstring("airGapped"))
	})

	It("does not absorb CISProfile changes (replace-required)", func() {
		h := newHandlers()
		current := baseRKE2Config(nil)
		desired := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.CISProfile = bootstrapv1.CIS
		})
		req := canUpdateMachineReq(baseMachine("v1.33.0"), baseMachine("v1.33.0"), current, desired)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigPatch.Patch)).NotTo(ContainSubstring("cisProfile"))
	})

	It("returns empty patches when there is no diff", func() {
		h := newHandlers()
		cfg := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.Files = []bootstrapv1.File{{Path: "/etc/test", Content: "same"}}
		})
		req := canUpdateMachineReq(
			baseMachine("v1.33.0"), baseMachine("v1.33.0"),
			cfg, cfg,
		)
		resp := &runtimehooksv1.CanUpdateMachineResponse{}
		h.DoCanUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.MachinePatch.Patch)).To(Equal("[]"))
		Expect(string(resp.BootstrapConfigPatch.Patch)).To(Equal("[]"))
	})
})

var _ = Describe("DoCanUpdateMachineSet", func() {
	It("absorbs version and Files changes at the template level", func() {
		h := newHandlers()
		currentBCT := baseRKE2ConfigTemplate(nil)
		desiredBCT := baseRKE2ConfigTemplate(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.Files = []bootstrapv1.File{{Path: "/etc/rke2-test", Content: "after"}}
		})
		req := canUpdateMachineSetReq(
			baseMachineSet("v1.33.0"), baseMachineSet("v1.33.1"),
			currentBCT, desiredBCT,
		)
		resp := &runtimehooksv1.CanUpdateMachineSetResponse{}
		h.DoCanUpdateMachineSet(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.MachineSetPatch.PatchType).To(Equal(runtimehooksv1.JSONPatchType))
		Expect(string(resp.MachineSetPatch.Patch)).To(ContainSubstring("v1.33.1"))
		Expect(resp.BootstrapConfigTemplatePatch.PatchType).To(Equal(runtimehooksv1.JSONPatchType))
		Expect(string(resp.BootstrapConfigTemplatePatch.Patch)).To(ContainSubstring("/etc/rke2-test"))
		Expect(resp.InfrastructureMachineTemplatePatch.IsDefined()).To(BeFalse())
	})

	It("does not absorb PreRKE2Commands changes at the template level (replace-required)", func() {
		h := newHandlers()
		currentBCT := baseRKE2ConfigTemplate(nil)
		desiredBCT := baseRKE2ConfigTemplate(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.PreRKE2Commands = []string{"echo hi"}
		})
		req := canUpdateMachineSetReq(baseMachineSet("v1.33.0"), baseMachineSet("v1.33.0"), currentBCT, desiredBCT)
		resp := &runtimehooksv1.CanUpdateMachineSetResponse{}
		h.DoCanUpdateMachineSet(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigTemplatePatch.Patch)).NotTo(ContainSubstring("preRKE2Commands"))
	})

	It("absorbs NodeLabels changes at the template level", func() {
		h := newHandlers()
		currentBCT := baseRKE2ConfigTemplate(nil)
		desiredBCT := baseRKE2ConfigTemplate(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.AgentConfig.NodeLabels = []string{"tier=workers"}
		})
		req := canUpdateMachineSetReq(baseMachineSet("v1.33.0"), baseMachineSet("v1.33.0"), currentBCT, desiredBCT)
		resp := &runtimehooksv1.CanUpdateMachineSetResponse{}
		h.DoCanUpdateMachineSet(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.BootstrapConfigTemplatePatch.Patch)).To(ContainSubstring("tier=workers"))
	})

	It("returns empty patches when there is no diff", func() {
		h := newHandlers()
		bct := baseRKE2ConfigTemplate(nil)
		req := canUpdateMachineSetReq(
			baseMachineSet("v1.33.0"), baseMachineSet("v1.33.0"),
			bct, bct,
		)
		resp := &runtimehooksv1.CanUpdateMachineSetResponse{}
		h.DoCanUpdateMachineSet(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(string(resp.MachineSetPatch.Patch)).To(Equal("[]"))
		Expect(string(resp.BootstrapConfigTemplatePatch.Patch)).To(Equal("[]"))
	})
})

var _ = Describe("DoUpdateMachine", func() {
	It("returns retry when the machine-plan Secret is not yet registered", func() {
		h := newHandlers() // no Secrets pre-populated
		req := updateMachineReq(baseMachine("v1.33.1"), baseRKE2Config(nil))
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.RetryAfterSeconds).To(BeNumerically(">", 0))
	})

	It("writes the upgrade plan to the Secret on the first call", func() {
		secret := machinePlanSecret(nil)
		h := newHandlers(secret)
		req := updateMachineReq(baseMachine("v1.33.1"), baseRKE2Config(nil))
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.RetryAfterSeconds).To(BeNumerically(">", 0))

		By("verifying the plan was written to the Secret")
		updated := &corev1.Secret{}
		Expect(h.client.Get(context.Background(), client.ObjectKeyFromObject(secret), updated)).To(Succeed())
		Expect(updated.Data["plan"]).NotTo(BeEmpty())
		Expect(string(updated.Data["plan"])).To(ContainSubstring("upgrade-rke2"))
	})

	It("returns retry while the plan is pending agent execution", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{"plan": planJSON})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.RetryAfterSeconds).To(BeNumerically(">", 0))
	})

	It("returns success when plan-state is succeeded (new-style agent)", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{
			"plan":             planJSON,
			plan.PlanStateKey: []byte(plan.PlanStateSucceeded),
		})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.RetryAfterSeconds).To(BeEquivalentTo(0))
	})

	It("returns success when appliedPlan matches the written plan (old-style agent)", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{
			"plan":        planJSON,
			"appliedPlan": planJSON,
		})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusSuccess))
		Expect(resp.RetryAfterSeconds).To(BeEquivalentTo(0))
	})

	It("returns failure when plan-state is failed (new-style agent)", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{
			"plan":             planJSON,
			plan.PlanStateKey: []byte(plan.PlanStateFailed),
		})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusFailure))
	})

	It("returns failure on old-style failure fields (failed-checksum + failure-count)", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{
			"plan":            planJSON,
			"failed-checksum": []byte(plan.PlanHash(planJSON)),
			"failure-count":   []byte("1"),
		})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		resp := &runtimehooksv1.UpdateMachineResponse{}
		h.DoUpdateMachine(context.Background(), req, resp)

		Expect(resp.Status).To(Equal(runtimehooksv1.ResponseStatusFailure))
	})

	It("does not rewrite the plan when the desired state has not changed", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		secret := machinePlanSecret(map[string][]byte{"plan": planJSON})
		h := newHandlers(secret)

		req := updateMachineReq(machine, cfg)
		h.DoUpdateMachine(context.Background(), req, &runtimehooksv1.UpdateMachineResponse{})
		h.DoUpdateMachine(context.Background(), req, &runtimehooksv1.UpdateMachineResponse{})

		By("verifying the Secret plan data is unchanged")
		updated := &corev1.Secret{}
		Expect(h.client.Get(context.Background(), client.ObjectKeyFromObject(secret), updated)).To(Succeed())
		Expect(updated.Data["plan"]).To(Equal(planJSON))
	})

	It("uses rke2-server unit for control-plane machines", func() {
		machine := cpMachine("v1.33.1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		Expect(string(planJSON)).To(ContainSubstring("rke2-server"))
		Expect(string(planJSON)).NotTo(ContainSubstring("rke2-agent"))
	})

	It("uses rke2-agent unit for worker machines", func() {
		machine := baseMachine("v1.33.1") // no control-plane label
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		Expect(string(planJSON)).To(ContainSubstring("rke2-agent"))
		Expect(string(planJSON)).NotTo(ContainSubstring("rke2-server"))
	})

	It("base64-encodes plain-text file content in the upgrade plan", func() {
		machine := baseMachine("v1.33.1")
		cfg := baseRKE2Config(func(s *bootstrapv1.RKE2ConfigSpec) {
			s.Files = []bootstrapv1.File{
				{Path: "/etc/rke2/config.yaml", Content: "token: abc123"},
			}
		})
		p := buildUpgradePlan(&machine, cfg)

		Expect(p.Files).To(HaveLen(1))
		Expect(p.Files[0].Path).To(Equal("/etc/rke2/config.yaml"))
		Expect(p.Files[0].Content).To(Equal("dG9rZW46IGFiYzEyMw=="))
	})

	It("includes the target version verbatim in the install script", func() {
		machine := baseMachine("v1.33.1+rke2r1")
		cfg := baseRKE2Config(nil)
		planJSON, err := json.Marshal(buildUpgradePlan(&machine, cfg))
		Expect(err).NotTo(HaveOccurred())

		Expect(string(planJSON)).To(ContainSubstring("v1.33.1+rke2r1"))
	})
})
