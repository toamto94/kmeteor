package webui

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kmeteoriov1alpha1 "github.com/toamto/kmeteor/api/v1alpha1"
)

//go:embed static/index.html
var indexHTML []byte

// Strike is one scheduled chaos event returned by the API.
type Strike struct {
	Name       string    `json:"name"`
	FireTime   time.Time `json:"fireTime"`
	ActionName string    `json:"actionName,omitempty"`
	Image      string    `json:"image,omitempty"`
	Command    []string  `json:"command,omitempty"`
	Args       []string  `json:"args,omitempty"`
}

// KMeteorInfo is the API response shape for a single KMeteor CR.
type KMeteorInfo struct {
	Name        string     `json:"name"`
	Namespace   string     `json:"namespace"`
	Lambda      float64    `json:"lambda"`
	Interval    string     `json:"interval"`
	ReconcileAt *time.Time `json:"reconcileAt,omitempty"`
	Strikes     []Strike   `json:"strikes"`
}

// Server serves the web UI and the /api/strikes JSON endpoint.
// It implements manager.Runnable so it can be registered with
// controller-runtime and started after the cache is synced.
type Server struct {
	client client.Client
	addr   string
}

// NewServer returns a Server backed by the given client, listening on addr.
func NewServer(c client.Client, addr string) *Server {
	return &Server{client: c, addr: addr}
}

// Start implements manager.Runnable; called after the manager cache is ready.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.addr,
		Handler: s.handler(),
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/strikes", s.handleStrikes)
	return mux
}

func (s *Server) handleStrikes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	kmList := &kmeteoriov1alpha1.KMeteorList{}
	if err := s.client.List(ctx, kmList); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]KMeteorInfo, 0, len(kmList.Items))
	for _, km := range kmList.Items {
		info := KMeteorInfo{
			Name:      km.Name,
			Namespace: km.Namespace,
			Lambda:    km.Spec.Lambda,
			Interval:  km.Spec.Interval.Duration.String(),
			Strikes:   []Strike{},
		}

		if km.Status.LastScheduleTime != nil {
			t := km.Status.LastScheduleTime.Time.Add(km.Spec.Interval.Duration)
			info.ReconcileAt = &t
		}

		cjList := &batchv1.CronJobList{}
		if err := s.client.List(ctx, cjList,
			client.InNamespace(km.Namespace),
			client.MatchingLabels{"kmeteor.io/owner": km.Name},
		); err == nil {
			for _, cj := range cjList.Items {
				ft := parseCronSchedule(cj.Spec.Schedule)
				if ft == nil {
					continue
				}
				strike := Strike{Name: cj.Name, FireTime: *ft}
				if containers := cj.Spec.JobTemplate.Spec.Template.Spec.Containers; len(containers) > 0 {
					c := containers[0]
					strike.Image = c.Image
					strike.Command = c.Command
					strike.Args = c.Args
					strike.ActionName = matchActionName(km.Spec.Actions, c.Image, c.Command)
				}
				info.Strikes = append(info.Strikes, strike)
			}
		}

		result = append(result, info)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// matchActionName returns the action name from the KMeteor spec whose image and
// command match the given values, or empty string if no match is found.
func matchActionName(actions []kmeteoriov1alpha1.ChaosAction, image string, command []string) string {
	for _, a := range actions {
		if a.Image == image && sliceEqual(a.Command, command) {
			return a.Name
		}
	}
	return ""
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// parseCronSchedule interprets the one-shot cron format "M H D Month *"
// produced by the controller and returns the corresponding absolute time.
func parseCronSchedule(schedule string) *time.Time {
	parts := strings.Fields(schedule)
	if len(parts) != 5 {
		return nil
	}
	minute, err1 := strconv.Atoi(parts[0])
	hour, err2 := strconv.Atoi(parts[1])
	day, err3 := strconv.Atoi(parts[2])
	month, err4 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil
	}
	t := time.Date(time.Now().Year(), time.Month(month), day, hour, minute, 0, 0, time.UTC)
	return &t
}
