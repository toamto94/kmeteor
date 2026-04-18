package main

import (
	"flag"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kmeteoriov1alpha1 "github.com/toamto/kmeteor/api/v1alpha1"
	"github.com/toamto/kmeteor/internal/controller"
	"github.com/toamto/kmeteor/internal/webui"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(kmeteoriov1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr           string
		probeAddr             string
		webUIAddr             string
		enableLeaderElect     bool
		jobServiceAccountName string
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address for the metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for health probes.")
	flag.StringVar(&webUIAddr, "web-ui-addr", "", "Address for the web UI (e.g. :8082). Empty disables the UI.")
	flag.BoolVar(&enableLeaderElect, "leader-elect", false, "Enable leader election for high availability.")
	flag.StringVar(&jobServiceAccountName, "job-service-account-name", "kmeteor-job",
		"ServiceAccount assigned to scheduled CronJobs; controls their RBAC independently of the operator.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElect,
		LeaderElectionID:       "kmeteor.io",
	})
	if err != nil {
		setupLog.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	if err = (&controller.KMeteorReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		JobServiceAccountName: jobServiceAccountName,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", "KMeteor")
		os.Exit(1)
	}

	if webUIAddr != "" {
		if err := mgr.Add(webui.NewServer(mgr.GetClient(), webUIAddr)); err != nil {
			setupLog.Error(err, "Unable to register web UI server")
			os.Exit(1)
		}
		setupLog.Info("Web UI enabled", "addr", webUIAddr)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
