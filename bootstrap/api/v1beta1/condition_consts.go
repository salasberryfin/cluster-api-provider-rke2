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

package v1beta1

import (
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// DataSecretAvailableCondition documents the status of the bootstrap secret generation process.
	//
	// NOTE: When the DataSecret generation starts the process completes immediately and within the
	// same reconciliation, so the user will always see a transition from Wait to Generated without having
	// to wait for the next reconciliation.
	DataSecretAvailableCondition clusterv1.ConditionType = "Available"
)

const (
	// DataSecretGenerationFailedReason (Severity=Warning) documents a RKE2Config controller detecting
	// an error while generating a data secret; those kind of errors are usually due to misconfigurations
	// and user intervention is required to get them fixed.
	DataSecretGenerationFailedReason string = "DataSecretGenerationFailed"

	// WaitingForClusterInfrastructureReason (Severity=Info) document a bootstrap secret generation process
	// waiting for the cluster infrastructure to be ready.
	//
	// NOTE: Having the cluster infrastructure ready is a pre-condition for starting to create machines.
	WaitingForClusterInfrastructureReason string = "WaitingForClusterInfrastructure"
)

const (
	// CertificatesAvailableCondition documents the status of the certificates generation process.
	CertificatesAvailableCondition clusterv1.ConditionType = "CertificatesAvailable"

	// CertificatesGenerationFailedReason documents a RKE2Config controller detecting
	// an error while generating certificates; those kind of errors are usually due to misconfigurations
	// and user intervention is required to get them fixed.
	CertificatesGenerationFailedReason string = "CertificateGenerationFailed"
)
