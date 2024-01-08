/*
Copyright 2022 SUSE.

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

package v1alpha1

import (
	bootstrapv1 "github.com/rancher-sandbox/cluster-api-provider-rke2/bootstrap/api/v1beta1"
	//utilconversion "sigs.k8s.io/cluster-api/util/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
)

// ConvertTo converts the v1alpha1 RKE2Config receiver to a v1beta1 RKE2Config.
func (r *RKE2Config) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*bootstrapv1.RKE2Config)

	if err := autoConvert_v1alpha1_RKE2Config_To_v1beta1_RKE2Config(r, dst, nil); err != nil {
		return err
	}

	// restored := &bootstrapv1.RKE2Config{}
	// if ok, err := utilconversion.UnmarshalData(r, restored); err != nil || !ok {
	// 	return err
	// }
	//dst.Spec.XXX = restored.Spec.XXXX

	return nil
}

// ConvertFrom converts the v1beta1 RKE2Config receiver to a v1alpha1 RKE2Config.
func (r *RKE2Config) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*bootstrapv1.RKE2Config)

	if err := autoConvert_v1beta1_RKE2Config_To_v1alpha1_RKE2Config(src, r, nil); err != nil {
		return err
	}

	// if err := utilconversion.MarshalData(src, r); err != nil {
	// 	return nil
	// }

	return nil
}

// ConvertTo converts the v1alpha1 RKE2ConfigList receiver to a v1beta1 RKEControlPlaneList.
func (r *RKE2ConfigList) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*bootstrapv1.RKE2ConfigList)

	return autoConvert_v1alpha1_RKE2ConfigList_To_v1beta1_RKE2ConfigList(r, dst, nil)
}

// ConvertFrom converts the v1beta1 RKE2ConfigList receiver to a v1alpha1 RKE2ConfigList.
func (r *RKE2ConfigList) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*bootstrapv1.RKE2ConfigList)

	return autoConvert_v1beta1_RKE2ConfigList_To_v1alpha1_RKE2ConfigList(src, r, nil)
}
