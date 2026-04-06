package controller

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kmeteoriov1alpha1 "github.com/toamto/kmeteor/api/v1alpha1"
)

const ownerLabel = "kmeteor.io/owner"

// KMeteorReconciler reconciles a KMeteor object.
type KMeteorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// ServiceAccountName is the name of the ServiceAccount the operator itself runs as.
	// Scheduled CronJobs will use this same account so they inherit its RBAC.
	ServiceAccountName string
}

//+kubebuilder:rbac:groups=kmeteor.io,resources=kmeteors,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kmeteor.io,resources=kmeteors/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kmeteor.io,resources=kmeteors/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch

func (r *KMeteorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	km := &kmeteoriov1alpha1.KMeteor{}
	if err := r.Get(ctx, req.NamespacedName, km); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	interval := km.Spec.Interval.Duration
	if interval <= 0 {
		logger.Info("Interval must be positive, skipping reconcile")
		return ctrl.Result{}, nil
	}

	// 1. Clean up CronJobs from the previous interval.
	if err := r.cleanupCronJobs(ctx, km); err != nil {
		return ctrl.Result{}, fmt.Errorf("cleanup: %w", err)
	}

	// 2. Sample event times for the next interval via the Poisson process.
	now := time.Now()
	eventTimes := poissonEventTimes(km.Spec.Lambda, interval, now)
	logger.Info("Scheduling Poisson events", "count", len(eventTimes), "lambda", km.Spec.Lambda, "interval", interval)

	// 3. Create a one-shot CronJob for each sampled event time.
	var scheduled []string
	for i, t := range eventTimes {
		name := fmt.Sprintf("%s-%d-%d", km.Name, now.Unix(), i)
		if err := r.createCronJob(ctx, km, name, t); err != nil {
			logger.Error(err, "Failed to create CronJob", "name", name, "scheduledFor", t)
			continue
		}
		logger.Info("Created CronJob", "name", name, "scheduledFor", t.Format(time.RFC3339))
		scheduled = append(scheduled, name)
	}

	// 4. Persist schedule summary in status.
	km.Status.LastScheduleTime = &metav1.Time{Time: now}
	km.Status.ScheduledJobs = scheduled
	if err := r.Status().Update(ctx, km); err != nil {
		return ctrl.Result{}, fmt.Errorf("status update: %w", err)
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

// cleanupCronJobs deletes all CronJobs owned by this KMeteor instance.
func (r *KMeteorReconciler) cleanupCronJobs(ctx context.Context, km *kmeteoriov1alpha1.KMeteor) error {
	list := &batchv1.CronJobList{}
	if err := r.List(ctx, list,
		client.InNamespace(km.Namespace),
		client.MatchingLabels{ownerLabel: km.Name},
	); err != nil {
		return err
	}

	for i := range list.Items {
		cj := &list.Items[i]
		propagation := metav1.DeletePropagationBackground
		if err := r.Delete(ctx, cj, &client.DeleteOptions{
			PropagationPolicy: &propagation,
		}); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete CronJob %s: %w", cj.Name, err)
		}
	}
	return nil
}

// poissonEventTimes generates event timestamps for one interval starting at `start`.
//
// Events arrive as a Poisson process with average rate lambda per interval.
// Inter-arrival times are sampled from Exp(lambda / intervalSeconds).
// Events that fall within (start, start+interval) are returned; the first minute
// is skipped so that the cron schedule has time to be registered.
func poissonEventTimes(lambda float64, interval time.Duration, start time.Time) []time.Time {
	var times []time.Time
	intervalSec := interval.Seconds()
	elapsed := 0.0
	// rate in events/second
	rate := lambda / intervalSec

	for {
		// Sample inter-arrival time: T ~ Exp(rate)  →  T = -ln(U) / rate
		interArrival := -math.Log(rand.Float64()) / rate
		elapsed += interArrival
		if elapsed >= intervalSec {
			break
		}
		t := start.Add(time.Duration(elapsed * float64(time.Second)))
		// Skip events too close to now — cron needs at least ~1 minute lead time.
		if t.After(start.Add(time.Minute)) {
			times = append(times, t)
		}
	}
	return times
}

// timeToCronSchedule converts an absolute time to a cron expression
// that fires once at that minute: "M H D Month *"
func timeToCronSchedule(t time.Time) string {
	return fmt.Sprintf("%d %d %d %d *", t.Minute(), t.Hour(), t.Day(), int(t.Month()))
}

// createCronJob creates a one-shot CronJob that fires at time t and prints "fired".
func (r *KMeteorReconciler) createCronJob(ctx context.Context, km *kmeteoriov1alpha1.KMeteor, name string, t time.Time) error {
	schedule := timeToCronSchedule(t)

	successLimit := int32(1)
	failedLimit := int32(1)

	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: km.Namespace,
			Labels: map[string]string{
				ownerLabel: km.Name,
			},
			// Garbage-collect CronJobs when the KMeteor CR is deleted.
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(km, kmeteoriov1alpha1.GroupVersion.WithKind("KMeteor")),
			},
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule,
			SuccessfulJobsHistoryLimit: &successLimit,
			FailedJobsHistoryLimit:     &failedLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							// Use the operator's ServiceAccount so jobs inherit its RBAC.
							ServiceAccountName: r.ServiceAccountName,
							RestartPolicy:      corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:    "event",
									Image:   "busybox:latest",
									Command: []string{"sh", "-c", `echo "fired"`},
								},
							},
						},
					},
				},
			},
		},
	}

	return r.Create(ctx, cj)
}

// SetupWithManager registers the controller with the manager.
func (r *KMeteorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kmeteoriov1alpha1.KMeteor{}).
		Complete(r)
}
