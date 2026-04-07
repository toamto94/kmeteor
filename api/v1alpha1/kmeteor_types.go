package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ChaosAction defines a single chaos workload executed by a scheduled CronJob.
type ChaosAction struct {
	// Name is a human-readable label for this action (used in logs and events).
	Name string `json:"name"`

	// Image is the container image used to execute the chaos command.
	Image string `json:"image"`

	// Command is the entrypoint for the chaos container.
	// +kubebuilder:validation:MinItems=1
	Command []string `json:"command"`

	// Args are additional arguments passed to Command.
	// +optional
	Args []string `json:"args,omitempty"`

	// Weight controls the relative selection probability when multiple actions are
	// defined. A higher value increases the chance of this action being chosen.
	// Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Weight int `json:"weight,omitempty"`
}

// KMeteorSpec defines the desired state of KMeteor.
type KMeteorSpec struct {
	// Lambda is the average number of events per Interval (Poisson process rate).
	// +kubebuilder:validation:Minimum=0.001
	Lambda float64 `json:"lambda"`

	// Interval is the time window over which Lambda events are expected on average.
	// The controller reconciles every Interval and schedules jobs for the next window.
	Interval metav1.Duration `json:"interval"`

	// Actions is the list of chaos workloads the controller may schedule.
	// Each CronJob event picks one action at random, weighted by Weight.
	// If empty, the job falls back to printing "fired" (a no-op useful for testing).
	// +optional
	Actions []ChaosAction `json:"actions,omitempty"`
}

// KMeteorStatus defines the observed state of KMeteor.
type KMeteorStatus struct {
	// LastScheduleTime is when the controller last generated the schedule.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// ScheduledJobs holds the names of CronJobs created in the current interval.
	// +optional
	ScheduledJobs []string `json:"scheduledJobs,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Lambda",type=number,JSONPath=`.spec.lambda`
//+kubebuilder:printcolumn:name="Interval",type=string,JSONPath=`.spec.interval`
//+kubebuilder:printcolumn:name="Scheduled",type=integer,JSONPath=`.status.scheduledJobs`,priority=1
//+kubebuilder:printcolumn:name="LastSchedule",type=date,JSONPath=`.status.lastScheduleTime`

// KMeteor is the Schema for the kmeteors API.
type KMeteor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KMeteorSpec   `json:"spec,omitempty"`
	Status KMeteorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KMeteorList contains a list of KMeteor.
type KMeteorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KMeteor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KMeteor{}, &KMeteorList{})
}
