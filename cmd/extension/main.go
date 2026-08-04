/*
Copyright 2026 SUSE.

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

package main

import (
	"flag"
	"os"
	goruntime "runtime"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	"sigs.k8s.io/cluster-api/exp/runtime/server"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/flags"

	bootstrapv1 "github.com/rancher/cluster-api-provider-rke2/bootstrap/api/v1beta2"
	"github.com/rancher/cluster-api-provider-rke2/cmd/extension/handlers/inplaceupdate"
	controlplanev1 "github.com/rancher/cluster-api-provider-rke2/controlplane/api/v1beta2"
	"github.com/rancher/cluster-api-provider-rke2/pkg/consts"
	"github.com/rancher/cluster-api-provider-rke2/version"
)

const (
	controllerName         = "caprke2-extension-manager"
	defaultRestConfigQPS   = 100
	defaultRestConfigBurst = 200
)

var (
	catalog  = runtimecatalog.New()
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	enableLeaderElection        bool
	leaderElectionLeaseDuration time.Duration
	leaderElectionRenewDeadline time.Duration
	leaderElectionRetryPeriod   time.Duration
	profilerAddress             string
	enableContentionProfiling   bool
	syncPeriod                  time.Duration
	restConfigQPS               float32
	restConfigBurst             int
	webhookPort                 int
	webhookCertDir              string
	webhookCertName             string
	webhookKeyName              string
	healthAddr                  string
	managerOptions              = flags.ManagerOptions{}
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(bootstrapv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	utilruntime.Must(runtimehooksv1.AddToCatalog(catalog))
	//+kubebuilder:scaffold:scheme
} //nolint:wsl

// InitFlags initializes the flags.
func InitFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")

	fs.DurationVar(&leaderElectionLeaseDuration, "leader-elect-lease-duration", consts.DefaultLeaderElectLeaseDuration,
		"Interval at which non-leader candidates will wait to force acquire leadership (duration string)")

	fs.DurationVar(&leaderElectionRenewDeadline, "leader-elect-renew-deadline", consts.DefaultLeaderElectRenewDeadline,
		"Duration that the leading controller manager will retry refreshing leadership before giving up (duration string)")

	fs.DurationVar(&leaderElectionRetryPeriod, "leader-elect-retry-period", consts.DefaultLeaderElectRetryPeriod,
		"Duration the LeaderElector clients should wait between tries of actions (duration string)")

	fs.StringVar(&profilerAddress, "profiler-address", "",
		"Bind address to expose the pprof profiler (e.g. localhost:6060)")

	fs.BoolVar(&enableContentionProfiling, "contention-profiling", false,
		"Enable block profiling.")

	fs.DurationVar(&syncPeriod, "sync-period", consts.DefaultSyncPeriod,
		"The minimum interval at which watched resources are reconciled (e.g. 15m)")

	fs.Float32Var(&restConfigQPS, "kube-api-qps", defaultRestConfigQPS,
		"Maximum queries per second from the extension server client to the Kubernetes API server.")

	fs.IntVar(&restConfigBurst, "kube-api-burst", defaultRestConfigBurst,
		"Maximum number of queries allowed in one burst from the extension server client to the Kubernetes API server.")

	fs.IntVar(&webhookPort, "webhook-port", consts.DefaultWebhookPort, "Webhook Server port")

	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/",
		"Webhook cert dir, only used when webhook-port is specified.")

	fs.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt",
		"Webhook cert filename.")

	fs.StringVar(&webhookKeyName, "webhook-key-name", "tls.key",
		"Webhook key filename.")

	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")

	flags.AddManagerOptions(fs, &managerOptions)

	feature.MutableGates.AddFlag(fs)
}

func main() {
	klog.InitFlags(nil)

	// Opt into fixed stderrthreshold behavior (kubernetes/klog#212).
	_ = flag.Set("legacy_stderr_threshold_behavior", "false")
	_ = flag.Set("stderrthreshold", "INFO")

	InitFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(klog.Background())

	if enableContentionProfiling {
		goruntime.SetBlockProfileRate(1)
	}

	restConfig := ctrl.GetConfigOrDie()
	restConfig.QPS = restConfigQPS
	restConfig.Burst = restConfigBurst
	restConfig.UserAgent = remote.DefaultClusterAPIUserAgent(controllerName)

	tlsOptions, metricsOptions, err := flags.GetManagerOptions(managerOptions)
	if err != nil {
		setupLog.Error(err, "Unable to start manager: invalid flags")
		os.Exit(1)
	}

	runtimeExtensionWebhookServer, err := server.New(server.Options{
		Port:     webhookPort,
		CertDir:  webhookCertDir,
		CertName: webhookCertName,
		KeyName:  webhookKeyName,
		TLSOpts:  tlsOptions,
		Catalog:  catalog,
	})
	if err != nil {
		setupLog.Error(err, "unable to create runtime extension webhook server")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                     scheme,
		LeaderElection:             enableLeaderElection,
		LeaderElectionID:           "caprke2-extension-leader-election",
		LeaseDuration:              &leaderElectionLeaseDuration,
		RenewDeadline:              &leaderElectionRenewDeadline,
		RetryPeriod:                &leaderElectionRetryPeriod,
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		HealthProbeBindAddress:     healthAddr,
		PprofBindAddress:           profilerAddress,
		Metrics:                    *metricsOptions,
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{
					&corev1.ConfigMap{},
					&corev1.Secret{},
				},
				Unstructured: true,
			},
		},
		WebhookServer: runtimeExtensionWebhookServer,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	setupChecks(mgr)
	setupInPlaceUpdateHookHandlers(mgr, runtimeExtensionWebhookServer)

	setupLog.Info("Starting manager", "version", version.Get().String())

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupChecks(mgr ctrl.Manager) {
	if err := mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		setupLog.Error(err, "unable to create ready check")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		setupLog.Error(err, "unable to create health check")
		os.Exit(1)
	}
}

func setupInPlaceUpdateHookHandlers(mgr ctrl.Manager, runtimeExtensionWebhookServer *server.Server) {
	h := inplaceupdate.NewExtensionHandlers(mgr.GetClient())

	if err := runtimeExtensionWebhookServer.AddExtensionHandler(server.ExtensionHandler{
		Hook:        runtimehooksv1.CanUpdateMachine,
		Name:        "can-update-machine",
		HandlerFunc: h.DoCanUpdateMachine,
	}); err != nil {
		setupLog.Error(err, "unable to add extension handler", "hook", "CanUpdateMachine")
		os.Exit(1)
	}

	if err := runtimeExtensionWebhookServer.AddExtensionHandler(server.ExtensionHandler{
		Hook:        runtimehooksv1.CanUpdateMachineSet,
		Name:        "can-update-machineset",
		HandlerFunc: h.DoCanUpdateMachineSet,
	}); err != nil {
		setupLog.Error(err, "unable to add extension handler", "hook", "CanUpdateMachineSet")
		os.Exit(1)
	}

	if err := runtimeExtensionWebhookServer.AddExtensionHandler(server.ExtensionHandler{
		Hook:        runtimehooksv1.UpdateMachine,
		Name:        "update-machine",
		HandlerFunc: h.DoUpdateMachine,
	}); err != nil {
		setupLog.Error(err, "unable to add extension handler", "hook", "UpdateMachine")
		os.Exit(1)
	}
}
