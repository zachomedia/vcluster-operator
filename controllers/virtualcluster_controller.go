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

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	vclusterv1alpha1 "github.com/zachomedia/vcluster-operator/api/v1alpha1"
)

// VirtualClusterReconciler reconciles a VirtualCluster object
type VirtualClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	// DefaultVirtualClusterImage is the default cluster image used
	// when it is unspecified in a VirtuaCluster resource.
	DefaultVirtualClusterImage = "rancher/k3s:v1.19.5-k3s2"

	// DefaultSyncerImage is the default syncher image used
	// when it is unspecified in a VirtualCluster resource.
	DefaultSyncerImage = "loftsh/vcluster:0.2.0"

	// ContainerNameVirtualCluster is the name of the virtual cluster
	// container on the generated StatefulSet.
	ContainerNameVirtualCluster = "virtual-cluster"

	// ContainerNameSyncer is the name of the syncher container
	// on the generated StatefulSet.
	ContainerNameSyncer = "syncer"

	// ClusterFinalizer is the name of the cluster finalizer.
	ClusterFinalizer = "vcluster.zacharyseguin.ca/finalizer"
)

// serviceCide contains the service range for the host cluster.
//
// TODO: Make this a command line argument and/or dynamically obtain this value.
var serviceCidr = "10.43.0.0/16"

// createFunc defines a function which will create an object
type createFunc func(*vclusterv1alpha1.VirtualCluster) client.Object

// reconcileFunc defines a function that will reconcile an object.
// Return `true` to trigger an update of the object in the cluster.
type reconcileFunc func(*vclusterv1alpha1.VirtualCluster, client.Object) bool

//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=virtualclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts;services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete;escalate;bind
//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=virtualclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=virtualclusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the VirtualCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *VirtualClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	// Look up the object requested for reconciliation
	cluster := &vclusterv1alpha1.VirtualCluster{}
	err := r.Get(ctx, req.NamespacedName, cluster)

	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("cluster not found", "cluster", req.NamespacedName)
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// ******** SERVICE ACCOUNT ********
	res, err := r.reconcileObject(ctx, req, cluster, &corev1.ServiceAccount{}, r.serviceAccountForVirtualCluster, noReconcile)
	if res != nil || err != nil {
		return *res, err
	}

	// ******** ROLE ********
	res, err = r.reconcileObject(ctx, req, cluster, &rbacv1.Role{}, r.roleForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
		actual := obj.(*rbacv1.Role)
		expected := r.roleForVirtualCluster(cluster).(*rbacv1.Role)

		if !reflect.DeepEqual(actual.Rules, expected.Rules) {
			actual.Rules = expected.Rules
			return true
		}

		return false
	})
	if res != nil || err != nil {
		return *res, err
	}

	// ******** ROLE BINDING ********
	res, err = r.reconcileObject(ctx, req, cluster, &rbacv1.RoleBinding{}, r.roleBindingForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
		actual := obj.(*rbacv1.RoleBinding)
		expected := r.roleBindingForVirtualCluster(cluster).(*rbacv1.RoleBinding)

		if !reflect.DeepEqual(actual.RoleRef, expected.RoleRef) || !reflect.DeepEqual(actual.Subjects, expected.Subjects) {
			actual.RoleRef = expected.RoleRef
			actual.Subjects = expected.Subjects
			return true
		}

		return false
	})
	if res != nil || err != nil {
		return *res, err
	}

	// ******** CLUSTER ROLE ********
	if cluster.Spec.NodeSync.Strategy != vclusterv1alpha1.VirtualClusterNodeSyncStrategyFake {
		res, err = r.reconcileObjectNamed(ctx, req, cluster, &rbacv1.ClusterRole{}, r.clusterRoleForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
			actual := obj.(*rbacv1.ClusterRole)
			expected := r.clusterRoleForVirtualCluster(cluster).(*rbacv1.ClusterRole)

			if !reflect.DeepEqual(actual.Rules, expected.Rules) {
				actual.Rules = expected.Rules
				return true
			}

			return false
		}, fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster)))
		if res != nil || err != nil {
			return *res, err
		}
	} else {
		obj := &rbacv1.ClusterRole{}
		if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster))}, obj); err == nil {
			log.Info("removing cluster role for cluster", "cluster", req.NamespacedName)
			err = r.Delete(ctx, obj)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// ******** CLUSTER ROLE BINDING ********
	if cluster.Spec.NodeSync.Strategy != vclusterv1alpha1.VirtualClusterNodeSyncStrategyFake {
		res, err = r.reconcileObjectNamed(ctx, req, cluster, &rbacv1.ClusterRoleBinding{}, r.clusterRoleBindingForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
			actual := obj.(*rbacv1.ClusterRoleBinding)
			expected := r.clusterRoleBindingForVirtualCluster(cluster).(*rbacv1.ClusterRoleBinding)

			if !reflect.DeepEqual(actual.RoleRef, expected.RoleRef) || !reflect.DeepEqual(actual.Subjects, expected.Subjects) {
				actual.RoleRef = expected.RoleRef
				actual.Subjects = expected.Subjects
				return true
			}

			return false
		}, fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster)))
		if res != nil || err != nil {
			return *res, err
		}
	} else {
		obj := &rbacv1.ClusterRoleBinding{}
		if err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster))}, obj); err == nil {
			log.Info("removing cluster role binding for cluster", "cluster", req.NamespacedName)
			err = r.Delete(ctx, obj)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// ******** SERVICE ********
	res, err = r.reconcileObject(ctx, req, cluster, &v1.Service{}, r.serviceForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
		actual := obj.(*corev1.Service)
		expected := r.serviceForVirtualCluster(cluster).(*corev1.Service)

		// Sync up ClusterIP field
		expected.Spec.ClusterIP = actual.Spec.ClusterIP
		expected.Spec.ClusterIPs = actual.Spec.ClusterIPs
		expected.Spec.IPFamilies = actual.Spec.IPFamilies
		expected.Spec.IPFamilyPolicy = actual.Spec.IPFamilyPolicy

		if !reflect.DeepEqual(actual.Spec, expected.Spec) {
			actual.Spec = expected.Spec
			return true
		}

		return false
	})
	if res != nil || err != nil {
		return *res, err
	}

	res, err = r.reconcileObjectNamed(ctx, req, cluster, &v1.Service{}, r.headlessServiceForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
		actual := obj.(*corev1.Service)
		expected := r.headlessServiceForVirtualCluster(cluster).(*corev1.Service)

		// Sync up ClusterIP field
		expected.Spec.ClusterIP = actual.Spec.ClusterIP
		expected.Spec.ClusterIPs = actual.Spec.ClusterIPs
		expected.Spec.IPFamilies = actual.Spec.IPFamilies
		expected.Spec.IPFamilyPolicy = actual.Spec.IPFamilyPolicy

		if !reflect.DeepEqual(actual.Spec, expected.Spec) {
			actual.Spec = expected.Spec
			return true
		}

		return false
	}, fmt.Sprintf("%s-headless", objectName(cluster)))
	if res != nil || err != nil {
		return *res, err
	}

	// ******** INGRESS ********
	if cluster.Spec.Ingress != nil {
		res, err = r.reconcileObject(ctx, req, cluster, &networkingv1.Ingress{}, r.ingressForCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
			actual := obj.(*networkingv1.Ingress)
			expected := r.ingressForCluster(cluster).(*networkingv1.Ingress)

			if !reflect.DeepEqual(actual.Spec, expected.Spec) {
				actual.Spec = expected.Spec
				return true
			}

			return false
		})
		if res != nil || err != nil {
			return *res, err
		}
	} else {
		ingress := &networkingv1.Ingress{}
		if err := r.Get(ctx, req.NamespacedName, ingress); err == nil {
			log.Info("removing ingress for cluster", "cluster", req.NamespacedName)
			err = r.Delete(ctx, ingress)
			if err != nil {
				return ctrl.Result{}, err
			}
		}

	}

	// ******** STATEFUL SET ********
	res, err = r.reconcileObject(ctx, req, cluster, &appsv1.StatefulSet{}, r.statefulSetForVirtualCluster, func(cluster *vclusterv1alpha1.VirtualCluster, obj client.Object) bool {
		actual := obj.(*appsv1.StatefulSet)
		expected := r.statefulSetForVirtualCluster(cluster).(*appsv1.StatefulSet)

		update := false

		// Containers
		if len(actual.Spec.Template.Spec.Containers) != len(expected.Spec.Template.Spec.Containers) {
			update = true
			actual.Spec.Template.Spec.Containers = expected.Spec.Template.Spec.Containers
		} else {
			updateContainers := false

			for indx, actualContainer := range actual.Spec.Template.Spec.Containers {
				expectedContainer := expected.Spec.Template.Spec.Containers[indx]

				if actualContainer.Name != expectedContainer.Name {
					updateContainers = true
					break
				}

				if actualContainer.Image != expectedContainer.Image {
					updateContainers = true
					break
				}

				if !reflect.DeepEqual(actualContainer.Command, expectedContainer.Command) {
					updateContainers = true
					break
				}

				if !reflect.DeepEqual(actualContainer.Args, expectedContainer.Args) {
					updateContainers = true
					break
				}

				if !reflect.DeepEqual(actualContainer.Ports, expectedContainer.Ports) {
					updateContainers = true
					break
				}

				if !reflect.DeepEqual(actualContainer.VolumeMounts, expectedContainer.VolumeMounts) {
					updateContainers = true
					break
				}
			}

			if updateContainers {
				update = true
				actual.Spec.Template.Spec.Containers = expected.Spec.Template.Spec.Containers
			}
		}

		if !reflect.DeepEqual(actual.Spec.Template.Spec.TerminationGracePeriodSeconds, expected.Spec.Template.Spec.TerminationGracePeriodSeconds) {
			update = true
			actual.Spec.Template.Spec.TerminationGracePeriodSeconds = expected.Spec.Template.Spec.TerminationGracePeriodSeconds
		}

		if actual.Spec.Template.Spec.ServiceAccountName != expected.Spec.Template.Spec.ServiceAccountName {
			update = true
			actual.Spec.Template.Spec.ServiceAccountName = expected.Spec.Template.Spec.ServiceAccountName
		}

		if !reflect.DeepEqual(actual.Spec.Template.Spec.Volumes, expected.Spec.Template.Spec.Volumes) {
			update = true
			actual.Spec.Template.Spec.Volumes = expected.Spec.Template.Spec.Volumes
		}

		if len(actual.Spec.VolumeClaimTemplates) != len(expected.Spec.VolumeClaimTemplates) {
			update = true
			actual.Spec.VolumeClaimTemplates = expected.Spec.VolumeClaimTemplates
		} else {
			updateTemplates := false

			for indx, actualTemplate := range actual.Spec.VolumeClaimTemplates {
				expectedTemplate := expected.Spec.VolumeClaimTemplates[indx]

				if actualTemplate.ObjectMeta.Name != expectedTemplate.ObjectMeta.Name {
					updateTemplates = true
					break
				}

				if !equality.Semantic.DeepDerivative(actualTemplate.Spec, expectedTemplate.Spec) {
					updateTemplates = true
					break
				}
			}

			if updateTemplates {
				update = true
				actual.Spec.VolumeClaimTemplates = expected.Spec.VolumeClaimTemplates
			}
		}

		return update
	})

	if res != nil || err != nil {
		return *res, err
	}

	// *** FINALIZER ***
	// Run finalizer if the object is to be deleted
	if cluster.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(cluster, ClusterFinalizer) {
			if err := r.finalizeVirtualCluster(ctx, cluster); err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(cluster, ClusterFinalizer)
			err := r.Update(ctx, cluster)
			if err != nil {
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	// Add finalizer, if unset
	if !controllerutil.ContainsFinalizer(cluster, ClusterFinalizer) {
		controllerutil.AddFinalizer(cluster, ClusterFinalizer)
		err = r.Update(ctx, cluster)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// finalizeVirtualCluster runs cleanup of resources that will not be garbage collected by Kubernetes.
func (r *VirtualClusterReconciler) finalizeVirtualCluster(ctx context.Context, cluster *vclusterv1alpha1.VirtualCluster) error {
	// Delete ClusterRole and ClusterRoleBinding
	clusterRole := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", cluster.Namespace, cluster.Name)}, clusterRole)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
	} else {
		err = r.Delete(ctx, clusterRole)
		if err != nil {
			return err
		}
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	err = r.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-%s", cluster.Namespace, cluster.Name)}, clusterRoleBinding)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
	} else {
		err = r.Delete(ctx, clusterRoleBinding)
		if err != nil {
			return err
		}
	}

	return nil
}

// noReconcile will always return `false` to never trigger reconciliation.
func noReconcile(_ *vclusterv1alpha1.VirtualCluster, _ client.Object) bool {
	return false
}

// reconcileObject reconciles an object created by the controller.
//
// The object reconciled will is named the default object name returned by `objectName(string)`
func (r *VirtualClusterReconciler) reconcileObject(
	ctx context.Context,
	req ctrl.Request,
	cluster *vclusterv1alpha1.VirtualCluster,
	obj client.Object,
	createFunc createFunc,
	reconcile reconcileFunc) (*ctrl.Result, error) {
	return r.reconcileObjectNamed(ctx, req, cluster, obj, createFunc, reconcile, objectName(cluster))
}

// reconcileObjectNamed reconciles the named object created by the controller.
func (r *VirtualClusterReconciler) reconcileObjectNamed(
	ctx context.Context,
	req ctrl.Request,
	cluster *vclusterv1alpha1.VirtualCluster,
	obj client.Object,
	createFunc createFunc,
	reconcile reconcileFunc,
	name string) (*ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, obj)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("creating object for cluster", "name", name, "object", obj.GetObjectKind(), "cluster", req.NamespacedName)

			// Create the statefulSet
			obj = createFunc(cluster)
			if err = r.Create(ctx, obj); err != nil {
				if errors.IsAlreadyExists(err) {
					log.Error(nil, "object for cluster already exists", "name", name, "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
					return &ctrl.Result{Requeue: true}, nil
				}

				log.Error(err, "failed to create object for cluster", "name", name, "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
				return &ctrl.Result{}, err
			}

			log.Info("created object for cluster", "name", name, "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
			return &ctrl.Result{Requeue: true}, nil
		} else {
			return &ctrl.Result{}, err
		}
	}

	// Run reconciliation of existing object
	if reconcile(cluster, obj) {
		log.Info("updating object for cluster", "object", "name", name, obj.GetObjectKind(), "cluster", req.NamespacedName)

		if err = r.Update(ctx, obj); err != nil {
			log.Error(err, "failed to update object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
			return &ctrl.Result{}, err
		}

		log.Info("updated object for cluster", "name", name, "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
		return &ctrl.Result{Requeue: true}, nil
	}

	// Don't do anything!
	return nil, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vclusterv1alpha1.VirtualCluster{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}

// toInt32Ptr converts an `int32` value as a pointer.
func toInt32Ptr(val int32) *int32 {
	return &val
}

// toInt64Ptr returns an `int64` value as a pointer.
func toInt64Ptr(val int64) *int64 {
	return &val
}

// labelsForVirtualCluster returns the object labels the given virtual cluster.
//
// All labels applied to a cluster are copied to the resources created by the controller.
func labelsForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) map[string]string {
	// Copy any labels from the cluster object istself
	labels := cluster.DeepCopy().Labels
	if labels == nil {
		labels = make(map[string]string)
	}

	// Add selector labels
	for k, v := range selectorLabelsForVirtualCluster(cluster) {
		labels[k] = v
	}

	// Add labels to track the "ownership" of these resources
	labels["app.kubernetes.io/managed-by"] = "vcluster-operator"
	labels["app.kubernetes.io/created-by"] = "vcluster-operator"

	// For vcluster cli
	labels["app"] = "vcluster"
	labels["release"] = cluster.Name

	return labels
}

// selectorLabelsForVirtualClsuter returns selector labels for a given virtual cluster.
func selectorLabelsForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "vcluster",
		"app.kubernetes.io/component": "control-plane",
		"app.kubernetes.io/instance":  cluster.Name,
	}
}

// objectName returns the standard name of objects created by the controller
// for the given virtual cluster.
func objectName(cluster *vclusterv1alpha1.VirtualCluster) string {
	// To prevent collision with default
	if cluster.Name == "default" {
		return fmt.Sprintf("vcluster-%s", cluster.Name)
	}

	return cluster.Name
}

// serviceAccountForVirtualCluster returns the ServiceAccount object for the given cluster.
func (r *VirtualClusterReconciler) serviceAccountForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
	}

	controllerutil.SetControllerReference(cluster, serviceAccount, r.Scheme)
	return serviceAccount
}

// roleForVirtualCluster returns the Role object for the given virtual cluster.
func (r *VirtualClusterReconciler) roleForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps", "secrets", "services", "services/proxy", "pods", "pods/proxy", "pods/attach", "pods/portforward", "pods/exec", "pods/log", "events", "endpoints", "persistentvolumeclaims"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"ingresses"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"statefulsets"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}

	controllerutil.SetControllerReference(cluster, role, r.Scheme)
	return role
}

// clusterRoleForVirtualCluster returns the ClusterRole object for the given virtual cluster.
func (r *VirtualClusterReconciler) clusterRoleForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster)),
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes", "nodes/status"},
				Verbs:     []string{"get", "watch", "list", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "nodes/proxy", "nodes/metrics", "nodes/stats", "persistentvolumes"},
				Verbs:     []string{"get", "watch", "list"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"scheduling.k8s.io"},
				Resources: []string{"priorityclasses"},
				Verbs:     []string{"*"},
			},
		},
	}

	return clusterRole
}

// roleBindingForVirtualCluster returns the RoleBinding object for the given virtual cluster.
func (r *VirtualClusterReconciler) roleBindingForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "Role",
			Name:     objectName(cluster),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: rbacv1.ServiceAccountKind,
				Name: objectName(cluster),
			},
		},
	}

	controllerutil.SetControllerReference(cluster, roleBinding, r.Scheme)
	return roleBinding
}

// clusterRoleBindingForVirtualCluster returns the ClusterRole object for the given virtual cluster.
func (r *VirtualClusterReconciler) clusterRoleBindingForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	roleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster)),
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     fmt.Sprintf("%s-%s", cluster.Namespace, objectName(cluster)),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      objectName(cluster),
				Namespace: cluster.Namespace,
			},
		},
	}

	return roleBinding
}

// serviceForVirtualCluster returns the Service object for the virtual cluster.
func (r *VirtualClusterReconciler) serviceForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromString("https"),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector:        selectorLabelsForVirtualCluster(cluster),
			SessionAffinity: v1.ServiceAffinityNone,
		},
	}

	controllerutil.SetControllerReference(cluster, service, r.Scheme)
	return service
}

// headlessServiceForVirtualCluster returns the headless Service object for the given cluster.
func (r *VirtualClusterReconciler) headlessServiceForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-headless", objectName(cluster)),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Spec: v1.ServiceSpec{
			Type:      v1.ServiceTypeClusterIP,
			ClusterIP: v1.ClusterIPNone,
			Ports: []v1.ServicePort{
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromString("https"),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector:        selectorLabelsForVirtualCluster(cluster),
			SessionAffinity: v1.ServiceAffinityNone,
		},
	}

	controllerutil.SetControllerReference(cluster, service, r.Scheme)
	return service
}

// ingressForCluster returns the Ingress object for the virtual cluster.
func (r *VirtualClusterReconciler) ingressForCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	pathTypePrefix := networkingv1.PathTypePrefix

	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: cluster.Spec.Ingress.IngressClassName,
			Rules: []networkingv1.IngressRule{
				{
					Host: cluster.Spec.Ingress.Hostname,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathTypePrefix,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: objectName(cluster),
											Port: networkingv1.ServiceBackendPort{
												Name: "https",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if cluster.Spec.Ingress.TLSSecretName != nil {
		ingress.Spec.TLS = []networkingv1.IngressTLS{
			{
				Hosts:      []string{cluster.Spec.Ingress.Hostname},
				SecretName: *cluster.Spec.Ingress.TLSSecretName,
			},
		}
	}

	controllerutil.SetControllerReference(cluster, ingress, r.Scheme)
	return ingress
}

// statefulSetForVirtualCluster returns the StatefulSet object for the given virtual cluster.
func (r *VirtualClusterReconciler) statefulSetForVirtualCluster(cluster *vclusterv1alpha1.VirtualCluster) client.Object {
	volumeModeFilesystem := corev1.PersistentVolumeFilesystem

	// Set node sync args
	nodeSyncArgs := []string{}
	switch cluster.Spec.NodeSync.Strategy {
	case vclusterv1alpha1.VirtualClusterNodeSyncStrategyFake:
		nodeSyncArgs = append(nodeSyncArgs, "--fake-nodes")
	case vclusterv1alpha1.VirtualClusterNodeSyncStrategyReal:
		nodeSyncArgs = append(nodeSyncArgs, "--fake-nodes=false")
	case vclusterv1alpha1.VirtualClusterNodeSyncStrategyRealAll:
		nodeSyncArgs = append(nodeSyncArgs, "--fake-nodes=false", "--sync-all-nodes")
	case vclusterv1alpha1.VirtualClusterNodeSyncStrategyRealLabelSelector:
		labels := []string{}
		for k, v := range cluster.Spec.NodeSync.SelectorLabels {
			labels = append(labels, fmt.Sprintf("%s=%s", k, v))
		}
		nodeSyncArgs = append(nodeSyncArgs, "--fake-nodes=false", fmt.Sprintf("--node-selector=%s", strings.Join(labels, ",")))

		if cluster.Spec.NodeSync.EnforceNodeSelector {
			nodeSyncArgs = append(nodeSyncArgs, "--enforce-node-selector")
		}
	}

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForVirtualCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    toInt32Ptr(1),
			ServiceName: fmt.Sprintf("%s-headless", objectName(cluster)),
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabelsForVirtualCluster(cluster),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForVirtualCluster(cluster),
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: toInt64Ptr(0),
					ServiceAccountName:            objectName(cluster),
					Containers: []corev1.Container{
						{
							Name:    ContainerNameVirtualCluster,
							Image:   cluster.Spec.ControlPlane.Image.String(),
							Command: []string{"/bin/k3s"},
							Args: append([]string{
								"server",
								"--write-kubeconfig=/k3-config/kube-config.yaml",
								"--data-dir=/data",
								"--disable=traefik,servicelb,metrics-server,local-storage",
								"--disable-network-policy",
								"--disable-agent",
								"--disable-scheduler",
								"--disable-cloud-controller",
								"--flannel-backend=none",
								"--kube-controller-manager-arg=controllers=*,-nodeipam,-nodelifecycle,-persistentvolume-binder,-attachdetach,-persistentvolume-expander,-cloud-node-lifecycle",
								fmt.Sprintf("--service-cidr=%s", serviceCidr),
							}, cluster.Spec.ControlPlane.ExtraArgs...),
							Ports: []v1.ContainerPort{
								{
									Name:          "https",
									ContainerPort: 8443,
									Protocol:      v1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
							},
						},
						{
							Name:  ContainerNameSyncer,
							Image: cluster.Spec.Syncer.Image.String(),
							Args: append([]string{
								fmt.Sprintf("--service-name=%s", objectName(cluster)),
								fmt.Sprintf("--suffix=%s", objectName(cluster)),
								fmt.Sprintf("--owning-statefulset=%s", objectName(cluster)),
								fmt.Sprintf("--out-kube-config-secret=%s", objectName(cluster)),
							}, append(nodeSyncArgs, cluster.Spec.Syncer.ExtraArgs...)...),
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: *resource.NewQuantity(5*1024*1024*1024, resource.BinarySI),
							},
						},
						VolumeMode: &volumeModeFilesystem,
					},
				},
			},
		},
	}

	controllerutil.SetControllerReference(cluster, statefulSet, r.Scheme)
	return statefulSet
}
