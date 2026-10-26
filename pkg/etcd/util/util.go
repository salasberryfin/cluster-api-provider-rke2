/*
Copyright 2024 SUSE.

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

// Package util implements etcd utility functions.
package util

import (
	"strings"

	"github.com/rancher/cluster-api-provider-rke2/pkg/etcd"
)

// NodeNameFromMember returns the node name from the etcd member name.
func NodeNameFromMember(member *etcd.Member) string {
	memberName := member.Name
	lastIndex := strings.LastIndex(memberName, "-")

	if lastIndex != -1 {
		memberName = memberName[:lastIndex]
	}

	return memberName
}

// MemberForName returns the etcd member with the matching name.
func MemberForName(members []*etcd.Member, name string) *etcd.Member {
	for _, m := range members {
		if strings.Contains(m.Name, name) {
			return m
		}
	}

	return nil
}
