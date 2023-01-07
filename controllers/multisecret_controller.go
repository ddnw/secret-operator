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

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	multiv1alpha1 "github.com/ddnw/secret-operator/api/v1alpha1"
)

const (
	multiSecName             = "multiSec"
	annotationOwnerName      = "multisecrets.multi.ddnw.ml/owner"
	annotationOwnerNamespace = "multisecrets.multi.ddnw.ml/namespace"
	FinalizerName            = "multisecrets.multi.ddnw.ml/finalizer"
)

// MultiSecretReconciler reconciles a MultiSecret object
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
type MultiSecretReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=multi.ddnw.ml,resources=multisecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=multi.ddnw.ml,resources=multisecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=multi.ddnw.ml,resources=multisecrets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=secrets/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MultiSecret object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *MultiSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrlRes ctrl.Result, ctrlErr error) {
	log := ctrllog.FromContext(ctx)

	// Get MultiSecret object
	mSecret := &multiv1alpha1.MultiSecret{}
	err := r.Get(ctx, req.NamespacedName, mSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("MultiSecret resource not found. Ignoring since object must be deleted")
			return reconcile.Result{}, nil
		}
		log.Error(err, "Failed to get MultiSecret")
		return reconcile.Result{}, err
	}

	nameSpaces, err := r.getNamespaces()
	if err != nil {
		log.Error(err, "Failed to get NameSpaces")
		return ctrl.Result{}, err
	}

	// Calculate Wanted Status
	sWantedStatus := 0
	existedSecrets := 0
	changed := false
	for _, ns := range nameSpaces {
		if nsInList(mSecret, ns) {
			sWantedStatus++
		}
	}

	// Update Status on reconcile exit
	defer func() {
		if ctrlErr == nil {
			if changed || sWantedStatus != mSecret.Status.Wanted || existedSecrets != mSecret.Status.Created {
				patch := client.MergeFrom(mSecret.DeepCopy())
				mSecret.Status.Wanted = sWantedStatus
				mSecret.Status.Created = existedSecrets
				mSecret.Status.ChangeTime = time.Now().Format(time.RFC3339)
				ctrlErr = r.Status().Patch(ctx, mSecret, patch)
			}
			if ctrlErr != nil {
				log.Error(ctrlErr, "Failed to update multiSecret Status",
					"Namespace", mSecret.Namespace, "Name", mSecret.Name)
			}
		}
	}()

	inFinalizeStage := false
	// Check Finalizer
	if mSecret.ObjectMeta.DeletionTimestamp.IsZero() {
		if !ctrlutil.ContainsFinalizer(mSecret, FinalizerName) {
			ctrlutil.AddFinalizer(mSecret, FinalizerName)
			if err := r.Update(ctx, mSecret); err != nil {
				return ctrl.Result{}, err
			}
			changed = true
		}
	} else {
		// The object is being deleted
		inFinalizeStage = true
		if ctrlutil.ContainsFinalizer(mSecret, FinalizerName) {
			// our finalizer is present, so lets handle any external dependency
			if err := r.deleteAllSecrets(ctx, genGlobalName(mSecret.Name, mSecret.Namespace, multiSecName), nameSpaces); err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return ctrl.Result{}, err
			}
			changed = true

			// remove our finalizer from the list and update it.
			ctrlutil.RemoveFinalizer(mSecret, FinalizerName)
			if err := r.Update(ctx, mSecret); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	for _, ns := range nameSpaces {
		wantDelete := true
		if nsInList(mSecret, ns) {
			wantDelete = false
		}
		secretExists := true
		foundSecret, err := r.findSecret(ctx, genGlobalName(mSecret.Name, mSecret.Namespace, multiSecName), ns)
		if err != nil {
			if errors.IsNotFound(err) {
				secretExists = false
			} else {
				log.Error(err, "Failed to get corev1.Secret", "NameSpace", ns, "Name", mSecret.Name)
				return ctrl.Result{}, err
			}
		}
		if secretExists {
			existedSecrets++
		}

		if secretExists && wantDelete {
			err := r.deleteSecret(ctx, foundSecret)
			if err != nil {
				return ctrl.Result{}, err
			}
			// corev1.Secret deleted successfully - return and requeue
			msg := fmt.Sprintf("Deleted corev1.Secret, NameSpace: %s, Name: %s", foundSecret.Namespace, foundSecret.Name)
			r.Recorder.Event(mSecret, "Normal", "Deleted", msg)
			existedSecrets--
			changed = true
			return ctrl.Result{Requeue: true}, nil
		}

		if !secretExists && !wantDelete && !inFinalizeStage {
			newSecret := r.newSecret(mSecret, ns)
			log.Info("Creating a new corev1.Secret", "Namespace", newSecret.Namespace, "Name", newSecret.Name)
			err = r.Create(ctx, newSecret)
			if err != nil {
				log.Error(err, "Failed to create new corev1.Secret", "Namespace", newSecret.Namespace, "Name", newSecret.Name)
				return ctrl.Result{}, err
			}
			// newSecret created successfully - return and requeue
			msg := fmt.Sprintf("Created corev1.Secret, NameSpace: %s, Name: %s", newSecret.Namespace, newSecret.Name)
			r.Recorder.Event(mSecret, "Normal", "Created", msg)
			existedSecrets++
			changed = true
			return ctrl.Result{Requeue: true}, nil
		}

		if secretExists && !wantDelete && !inFinalizeStage {
			// Implement if secret data outdated
		}

	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MultiSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&multiv1alpha1.MultiSecret{}).
		Watches(
			&source.Kind{Type: &corev1.Secret{}},
			handler.EnqueueRequestsFromMapFunc(r.secretHandlerFunc),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&source.Kind{Type: &corev1.Namespace{}},
			handler.Funcs{CreateFunc: r.nsHandlerFunc},
		).
		Complete(r)
}

func (r *MultiSecretReconciler) secretHandlerFunc(a client.Object) []reconcile.Request {
	anno := a.GetAnnotations()
	name, ok := anno[annotationOwnerName]
	namespace, ok2 := anno[annotationOwnerNamespace]
	if ok && ok2 {
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				},
			},
		}
	}
	return []reconcile.Request{}
}

func (r *MultiSecretReconciler) nsHandlerFunc(e event.CreateEvent, q workqueue.RateLimitingInterface) {
	multiSecretList := &multiv1alpha1.MultiSecretList{}
	err := r.List(context.TODO(), multiSecretList)
	if err != nil {
		return
	}
	for _, ms := range multiSecretList.Items {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      ms.Name,
			Namespace: ms.Namespace,
		}})
	}
}

func (r *MultiSecretReconciler) deleteAllSecrets(ctx context.Context, name string, nss []string) error {
	for _, ns := range nss {
		err := r.deleteSecretByName(ctx, name, ns)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *MultiSecretReconciler) findSecret(ctx context.Context, name, ns string) (*corev1.Secret, error) {
	foundSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, foundSecret)
	return foundSecret, err
}

func (r *MultiSecretReconciler) deleteSecret(ctx context.Context, secret *corev1.Secret) error {
	log := ctrllog.FromContext(ctx)
	err := r.Delete(ctx, secret)
	if errors.IsNotFound(err) {
		log.Info("corev1.Secret resource not found. Ignoring since object must be deleted",
			"NameSpace", secret.Namespace, "Name", secret.Name)
		return nil
	}
	if err != nil {
		log.Error(err, "Failed to delete corev1.Secret",
			"NameSpace", secret.Namespace, "Name", secret.Name)
		return err
	}
	return nil
}

func (r *MultiSecretReconciler) deleteSecretByName(ctx context.Context, name, ns string) error {
	log := ctrllog.FromContext(ctx)
	log.Info("Deleting corev1.Secret", "NameSpace", ns, "Name", name)
	foundSecret, err := r.findSecret(ctx, name, ns)
	if errors.IsNotFound(err) {
		log.Info("Deleting corev1.Secret, not exists", "NameSpace", ns, "Name", name)
		return nil
	}
	if err != nil {
		return err
	}
	return r.deleteSecret(ctx, foundSecret)
}

func (r *MultiSecretReconciler) updateStatus(ctx context.Context, s *multiv1alpha1.MultiSecret, wantedC, createdC int) error {

	return nil
}

func (r *MultiSecretReconciler) getNamespaces() ([]string, error) {
	//Get nameSpaces List
	ctx := context.TODO()
	foundNS := &corev1.NamespaceList{}
	err := r.List(ctx, foundNS)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, ns := range foundNS.Items {
		// Don't add nameSpace on deletion
		if ns.ObjectMeta.DeletionTimestamp.IsZero() {
			result = append(result, ns.Name)
		}
	}
	return result, nil
}

func (r *MultiSecretReconciler) newSecret(ms *multiv1alpha1.MultiSecret, ns string) *corev1.Secret {
	msNew := ms.DeepCopy()
	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:        genGlobalName(ms.Name, ms.Namespace, multiSecName),
			Namespace:   ns,
			Labels:      msNew.Labels,
			Annotations: addOwnerAnnotations(msNew, msNew.Annotations),
		},
		Data:       msNew.Spec.Data,
		StringData: msNew.Spec.StringData,
		Type:       corev1.SecretType(msNew.Spec.Type),
	}
	return secret
}

func genGlobalName(s ...string) string {
	return strings.ToLower(strings.Join(s, "."))
}

func addOwnerAnnotations(ms *multiv1alpha1.MultiSecret, m map[string]string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[annotationOwnerName] = ms.Name
	m[annotationOwnerNamespace] = ms.Namespace
	return m
}

func nsInList(mSecret *multiv1alpha1.MultiSecret, ns string) bool {
	return true
}
