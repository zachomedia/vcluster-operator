/*
Copyright 2021.

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

package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	DefaultClusterImage = "rancher/k3s:v1.19.5-k3s2"
	DefaultSyncerImage  = "loftsh/vcluster:0.2.0"
)

const serviceCidr = "10.43.0.0/16"

//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=vcluster.zacharyseguin.ca,resources=clusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Cluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// log := ctrllog.FromContext(ctx)

	// Look up the object requested for reconciliation
	cluster := &vclusterv1alpha1.Cluster{}
	err := r.Get(ctx, req.NamespacedName, cluster)

	if err != nil {
		return ctrl.Result{}, err
	}

	// ******** SERVICE ACCOUNT ********
	res, err := r.reconcileObject(ctx, req, cluster, &corev1.ServiceAccount{}, r.serviceAccountForCluster, noReconcile)
	if res != nil || err != nil {
		return *res, err
	}

	// ******** ROLE ********
	res, err = r.reconcileObject(ctx, req, cluster, &rbacv1.Role{}, r.roleForCluster, noReconcile)
	if res != nil || err != nil {
		return *res, err
	}

	// ******** ROLE BINDING ********
	res, err = r.reconcileObject(ctx, req, cluster, &rbacv1.RoleBinding{}, r.roleBindingForCluster, noReconcile)
	if res != nil || err != nil {
		return *res, err
	}

	// ******** SERVICE ********
	res, err = r.reconcileObject(ctx, req, cluster, &v1.Service{}, r.serviceForCluster, noReconcile)
	if res != nil || err != nil {
		return *res, err
	}

	res, err = r.reconcileObjectNamed(ctx, req, cluster, &v1.Service{}, r.headlessServiceForCluster, noReconcile, fmt.Sprintf("%s-headless", objectName(cluster)))
	if res != nil || err != nil {
		return *res, err
	}

	// ******** STATEFUL SET ********
	res, err = r.reconcileObject(ctx, req, cluster, &appsv1.StatefulSet{}, r.statefulSetForCluster, func(obj client.Object) bool {
		update := false
		statefulSet := obj.(*appsv1.StatefulSet)

		for indx, container := range statefulSet.Spec.Template.Spec.Containers {
			if container.Name == "virtual-cluster" {
				if container.Image != clusterImage(cluster) {
					update = true
					statefulSet.Spec.Template.Spec.Containers[indx].Image = clusterImage(cluster)
				}
			}
		}

		return update
	})

	if res != nil || err != nil {
		return *res, err
	}

	return ctrl.Result{}, nil
}

func noReconcile(_ client.Object) bool {
	return false
}

func (r *ClusterReconciler) reconcileObject(
	ctx context.Context,
	req ctrl.Request,
	cluster *vclusterv1alpha1.Cluster,
	obj client.Object,
	createFunc func(cluster *vclusterv1alpha1.Cluster) client.Object,
	reconcile func(obj client.Object) bool) (*ctrl.Result, error) {
	return r.reconcileObjectNamed(ctx, req, cluster, obj, createFunc, reconcile, objectName(cluster))
}

func (r *ClusterReconciler) reconcileObjectNamed(
	ctx context.Context,
	req ctrl.Request,
	cluster *vclusterv1alpha1.Cluster,
	obj client.Object,
	createFunc func(cluster *vclusterv1alpha1.Cluster) client.Object,
	reconcile func(obj client.Object) bool,
	name string) (*ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, obj)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("creating object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)

			// Create the statefulSet
			obj = createFunc(cluster)
			if err = r.Create(ctx, obj); err != nil {
				if errors.IsAlreadyExists(err) {
					log.Error(nil, "object for cluster already exists", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
					return &ctrl.Result{Requeue: true}, nil
				}

				log.Error(err, "failed to create object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
				return &ctrl.Result{}, err
			}

			log.Info("created object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
			return &ctrl.Result{Requeue: true}, nil
		} else {
			return &ctrl.Result{}, err
		}
	}

	// Run reconciliation of existing object
	if reconcile(obj) {
		log.Info("updating object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)

		if err = r.Update(ctx, obj); err != nil {
			log.Error(err, "failed to update object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
			return &ctrl.Result{}, err
		}

		log.Info("updated object for cluster", "object", obj.GetObjectKind(), "cluster", req.NamespacedName)
		return &ctrl.Result{Requeue: true}, nil
	}

	// Don't do anything!
	return nil, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vclusterv1alpha1.Cluster{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}

func toIntPtr(val int) *int {
	return &val
}

func toInt32Ptr(val int32) *int32 {
	return &val
}

func toInt64Ptr(val int64) *int64 {
	return &val
}

func clusterImage(cluster *vclusterv1alpha1.Cluster) string {
	if cluster.Spec.Image != "" {
		return cluster.Spec.Image
	}

	return DefaultClusterImage
}

func syncerImage(cluster *vclusterv1alpha1.Cluster) string {
	return DefaultSyncerImage
}

func labelsForCluster(cluster *vclusterv1alpha1.Cluster) map[string]string {
	labels := cluster.DeepCopy().Labels
	if labels == nil {
		labels = make(map[string]string)
	}

	labels["app.kubernetes.io/name"] = "vcluster"
	labels["app.kubernetes.io/component"] = "control-plane"
	labels["app.kubernetes.io/instance"] = cluster.Name
	// labels["app.kubernetes.io/version"] = cluster.Spec.Image
	labels["app.kubernetes.io/managed-by"] = "vcluster-operator"
	labels["app.kubernetes.io/created-by"] = "vcluster-operator"

	// For vcluster cli
	labels["app"] = "vcluster"
	labels["release"] = cluster.Name

	return labels
}

func selectorLabelsForCluster(cluster *vclusterv1alpha1.Cluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "vcluster",
		"app.kubernetes.io/component": "control-plane",
		"app.kubernetes.io/instance":  cluster.Name,
	}
}

func objectName(cluster *vclusterv1alpha1.Cluster) string {
	// To prevent collision with default
	if cluster.Name == "default" {
		return fmt.Sprintf("vcluster-%s", cluster.Name)
	}

	return cluster.Name
}

func (r *ClusterReconciler) serviceAccountForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
			Annotations: cluster.Annotations,
		},
	}

	controllerutil.SetControllerReference(cluster, serviceAccount, r.Scheme)
	return serviceAccount
}

func (r *ClusterReconciler) roleForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
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

func (r *ClusterReconciler) roleBindingForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
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

func (r *ClusterReconciler) serviceForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
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
			Selector: selectorLabelsForCluster(cluster),
		},
	}

	controllerutil.SetControllerReference(cluster, service, r.Scheme)
	return service
}

func (r *ClusterReconciler) headlessServiceForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-headless", objectName(cluster)),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
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
			Selector: selectorLabelsForCluster(cluster),
		},
	}

	controllerutil.SetControllerReference(cluster, service, r.Scheme)
	return service
}

func (r *ClusterReconciler) statefulSetForCluster(cluster *vclusterv1alpha1.Cluster) client.Object {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        objectName(cluster),
			Namespace:   cluster.Namespace,
			Labels:      labelsForCluster(cluster),
			Annotations: cluster.Annotations,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    toInt32Ptr(1),
			ServiceName: fmt.Sprintf("%s-headless", objectName(cluster)),
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabelsForCluster(cluster),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForCluster(cluster),
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: toInt64Ptr(0),
					ServiceAccountName:            objectName(cluster),
					Containers: []corev1.Container{
						{
							Name:    "virtual-cluster",
							Image:   clusterImage(cluster),
							Command: []string{"/bin/k3s"},
							Args: []string{
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
							},
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
							Name:  "syncer",
							Image: syncerImage(cluster),
							Args: []string{
								fmt.Sprintf("--service-name=%s", objectName(cluster)),
								fmt.Sprintf("--suffix=%s", objectName(cluster)),
								fmt.Sprintf("--owning-statefulset=%s", objectName(cluster)),
								fmt.Sprintf("--out-kube-config-secret=%s", objectName(cluster)),
							},
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
					},
				},
			},
		},
	}

	controllerutil.SetControllerReference(cluster, statefulSet, r.Scheme)
	return statefulSet
}
