/*
Copyright 2021 Zachary Seguin

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VirtualClusterSpec defines the desired state of VirtualCluster
type VirtualClusterSpec struct {
	// Defines the control plane specification
	// +kubebuilder:default={image:{repository:"rancher/k3s", tag:"latest"}}
	ControlPlane VirtualClusterComponentSpec `json:"controlPlane,omitempty"`

	// Defines the syncer specification
	// +kubebuilder:default={image:{repository:"loftsh/vcluster", tag:"latest"}}
	Syncer VirtualClusterComponentSpec `json:"syncer,omitempty"`

	// Define how nodes are synced into the virtual cluster
	//
	// Note: If using a strategy other than "VirtualClusterNodeSyncStrategyLabelSelector",
	// then a ClusterRole will be created.
	// +kubebuilder:default={strategy:"Fake"}
	NodeSync VirtualClusterNodeSyncSpec `json:"nodeSync,omitempty"`

	// Specify the ingress template for the cluster
	// +optional
	Ingress *VirtualClusterIngressSpec `json:"ingress,omitempty"`
}

// VirtualClusterNodeSyncSpec defines how nodes are synced into the virtual cluster
type VirtualClusterNodeSyncSpec struct {
	// Node sync strategy
	//
	// Note: If set to VirtualClusterNodeSyncStrategyRealLabelSelector,
	// then the SelectorLabels must be provided
	// +kubebuilder:validation:Enum=Fake;Real;RealAll;RealLabelSelector
	Strategy VirtualClusterNodeSyncStrategy `json:"strategy"`

	// SelectorLabels when strategy is set to VirtualClusterNodeSyncStrategyLabelSelector
	// +optional
	SelectorLabels map[string]string `json:"selectorLabels,omitempty"`

	// When using VirtualClusterNodeSyncStrategyRealLabelSelector, enforce the node
	// selector for scheduling of pods
	// +optional
	EnforceNodeSelector bool `json:"enforceNodeSelector,omitempty"`
}

type VirtualClusterNodeSyncStrategy string

const (
	VirtualClusterNodeSyncStrategyFake              VirtualClusterNodeSyncStrategy = "Fake"
	VirtualClusterNodeSyncStrategyReal              VirtualClusterNodeSyncStrategy = "Real"
	VirtualClusterNodeSyncStrategyRealAll           VirtualClusterNodeSyncStrategy = "RealAll"
	VirtualClusterNodeSyncStrategyRealLabelSelector VirtualClusterNodeSyncStrategy = "RealLabelSelector"
)

// VirtualClusterComponentSpec defines the desired state of a component of the Virtual Cluster
type VirtualClusterComponentSpec struct {
	// The image of the virtual cluster container
	Image VirtualClusterImageSpec `json:"image,omitempty"`

	// Extra arguments provided to the virtual cluster
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

// VirtualClusterIngressSpec defines the ingress specification for the virtual cluster
type VirtualClusterIngressSpec struct {
	IngressClassName *string `json:"ingressClassName,omitempty"`
	Hostname         string  `json:"hostname"`
	TLSSecretName    *string `json:"tlsSecretName,omitempty"`
}

type VirtualClusterImageSpec struct {
	// The image repository
	Repository string `json:"repository,omitempty"`

	// The image tag
	Tag string `json:"tag,omitempty"`
}

// String returns the image spec as an image string
func (img VirtualClusterImageSpec) String() string {
	return fmt.Sprintf("%s:%s", img.Repository, img.Tag)
}

// VirtualClusterStatus defines the observed state of VirtualCluster
type VirtualClusterStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=virtualclusters,shortName=vc;vcluster
//+kubebuilder:printcolumn:name="Cluster Image",type="string",JSONPath=".spec.cluster.image.tag",description="The image of the control plane"
//+kubebuilder:printcolumn:name="Syncer Image",type="string",JSONPath=".spec.syncer.image.tag",description="The image of the syncer"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of this resource"

// VirtualCluster is the Schema for the virtualclusters API
type VirtualCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualClusterSpec   `json:"spec"`
	Status VirtualClusterStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualClusterList contains a list of VirtualCluster
type VirtualClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VirtualCluster{}, &VirtualClusterList{})
}
