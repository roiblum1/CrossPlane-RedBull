/*
Copyright 2025 The Crossplane Authors.

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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// PortParameters defines the port configuration
type PortParameters struct {
	// Type is the protocol type (tcp, udp)
	// +kubebuilder:validation:Enum=tcp;udp
	Type string `json:"type"`

	// Number is the port number
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Number int `json:"number"`
}

// PortOrderParameters are the configurable fields of a PortOrder.
type PortOrderParameters struct {
	// Source is the source network CIDR
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$`
	Source string `json:"source"`

	// Destination is the destination network CIDR
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$`
	Destination string `json:"destination"`

	// Ports is the list of ports to open
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Ports []PortParameters `json:"ports"`

	// APIEndpoint is the endpoint for the orders API
	// +optional
	// +kubebuilder:default="https://api.example.com/orders"
	APIEndpoint string `json:"apiEndpoint,omitempty"`
}

// PortOrderObservation are the observable fields of a PortOrder.
type PortOrderObservation struct {
	// OrderID is the ID assigned by the API
	OrderID string `json:"orderId,omitempty"`

	// Status is the current status of the order
	Status string `json:"status,omitempty"`

	// LastRequestTime is when the order was last submitted
	LastRequestTime *metav1.Time `json:"lastRequestTime,omitempty"`

	// LastResponseStatus is the HTTP status code of the last response
	LastResponseStatus int `json:"lastResponseStatus,omitempty"`
}

// A PortOrderSpec defines the desired state of a PortOrder.
type PortOrderSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       PortOrderParameters `json:"forProvider"`
}

// A PortOrderStatus represents the observed state of a PortOrder.
type PortOrderStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          PortOrderObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A PortOrder represents a request to open ports between network segments.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="SOURCE",type="string",JSONPath=".spec.forProvider.source"
// +kubebuilder:printcolumn:name="DESTINATION",type="string",JSONPath=".spec.forProvider.destination"
// +kubebuilder:printcolumn:name="STATUS",type="string",JSONPath=".status.atProvider.status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,network}
type PortOrder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PortOrderSpec   `json:"spec"`
	Status PortOrderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PortOrderList contains a list of PortOrder
type PortOrderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PortOrder `json:"items"`
}

// PortOrder type metadata.
var (
	PortOrderKind             = reflect.TypeOf(PortOrder{}).Name()
	PortOrderGroupKind        = schema.GroupKind{Group: Group, Kind: PortOrderKind}.String()
	PortOrderKindAPIVersion   = PortOrderKind + "." + SchemeGroupVersion.String()
	PortOrderGroupVersionKind = SchemeGroupVersion.WithKind(PortOrderKind)
)

func init() {
	SchemeBuilder.Register(&PortOrder{}, &PortOrderList{})
}

// -----------------------------------------------------------------------------
// Manual DeepCopy implementations
// -----------------------------------------------------------------------------

// DeepCopyInto for PortOrder copies the receiver into out.
func (in *PortOrder) DeepCopyInto(out *PortOrder) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy for PortOrder creates a new deep copy.
func (in *PortOrder) DeepCopy() *PortOrder {
	if in == nil {
		return nil
	}
	out := new(PortOrder)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject makes PortOrder implement runtime.Object.
func (in *PortOrder) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

// -----------------------------------------------------------------------------
// PortOrderSpec DeepCopy
// -----------------------------------------------------------------------------

func (in *PortOrderSpec) DeepCopyInto(out *PortOrderSpec) {
	*out = *in
	in.ResourceSpec.DeepCopyInto(&out.ResourceSpec)
	if in.ForProvider.Ports != nil {
		out.ForProvider.Ports = make([]PortParameters, len(in.ForProvider.Ports))
		copy(out.ForProvider.Ports, in.ForProvider.Ports)
	}
}

// -----------------------------------------------------------------------------
// PortOrderStatus DeepCopy
// -----------------------------------------------------------------------------

func (in *PortOrderStatus) DeepCopyInto(out *PortOrderStatus) {
	*out = *in
	in.ResourceStatus.DeepCopyInto(&out.ResourceStatus)
	in.AtProvider.DeepCopyInto(&out.AtProvider)
}

// -----------------------------------------------------------------------------
// PortOrderObservation DeepCopy
// -----------------------------------------------------------------------------

func (in *PortOrderObservation) DeepCopyInto(out *PortOrderObservation) {
	*out = *in
	if in.LastRequestTime != nil {
		out.LastRequestTime = in.LastRequestTime.DeepCopy()
	}
}

// -----------------------------------------------------------------------------
// PortOrderList DeepCopy
// -----------------------------------------------------------------------------

func (in *PortOrderList) DeepCopyInto(out *PortOrderList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]PortOrder, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *PortOrderList) DeepCopy() *PortOrderList {
	if in == nil {
		return nil
	}
	out := new(PortOrderList)
	in.DeepCopyInto(out)
	return out
}

func (in *PortOrderList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}
