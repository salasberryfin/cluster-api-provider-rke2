/*
Copyright 2025 SUSE.

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

package contract

// Path defines a how to access a field in an Unstructured object.
type Path []string

// Append a field name to a path.
func (p Path) Append(k string) Path {
	return append(p, k)
}

// IsParentOf check if one path is Parent of the other.
func (p Path) IsParentOf(other Path) bool {
	if len(p) >= len(other) {
		return false
	}

	for i := range p {
		if p[i] != other[i] {
			return false
		}
	}

	return true
}

// Equal check if two path are equal (exact match).
func (p Path) Equal(other Path) bool {
	if len(p) != len(other) {
		return false
	}

	for i := range p {
		if p[i] != other[i] {
			return false
		}
	}

	return true
}

// Overlaps return true if two paths are Equal or one IsParentOf the other.
func (p Path) Overlaps(other Path) bool {
	return other.Equal(p) || other.IsParentOf(p) || p.IsParentOf(other)
}
