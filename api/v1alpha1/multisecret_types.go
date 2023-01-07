/*
Copyright 2023.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type SecretType string

// MultiSecretSpec defines the desired state of MultiSecret
type MultiSecretSpec struct {
	Data       map[string][]byte `json:"data,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"`
	Type       SecretType        `json:"type,omitempty"`
}

// MultiSecretStatus defines the observed state of MultiSecret
type MultiSecretStatus struct {
	Wanted     int    `json:"wanted"`
	Created    int    `json:"created"`
	ChangeTime string `json:"change_time,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Wanted",type=integer,JSONPath=`.status.wanted`
//+kubebuilder:printcolumn:name="Created",type=integer,JSONPath=`.status.created`
//+kubebuilder:printcolumn:name="ChangeTime",type=string,JSONPath=`.status.change_time`

// MultiSecret is the Schema for the multisecrets API
type MultiSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultiSecretSpec   `json:"spec,omitempty"`
	Status MultiSecretStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MultiSecretList contains a list of MultiSecret
type MultiSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiSecret{}, &MultiSecretList{})
}
