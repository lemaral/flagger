package main

import (
	"flag"
	"log"
	"time"

	_ "github.com/istio/glog"
	sharedclientset "github.com/knative/pkg/client/clientset/versioned"
	"github.com/knative/pkg/signals"
	clientset "github.com/stefanprodan/flagger/pkg/client/clientset/versioned"
	informers "github.com/stefanprodan/flagger/pkg/client/informers/externalversions"
	"github.com/stefanprodan/flagger/pkg/controller"
	"github.com/stefanprodan/flagger/pkg/logging"
	"github.com/stefanprodan/flagger/pkg/server"
	"github.com/stefanprodan/flagger/pkg/version"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	masterURL           string
	kubeconfig          string
	metricsServer       string
	controlLoopInterval time.Duration
	logLevel            string
	port                string
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&metricsServer, "metrics-server", "http://prometheus:9090", "Prometheus URL")
	flag.DurationVar(&controlLoopInterval, "control-loop-interval", 10*time.Second, "wait interval between rollouts")
	flag.StringVar(&logLevel, "log-level", "debug", "Log level can be: debug, info, warning, error.")
	flag.StringVar(&port, "port", "8080", "Port to listen on.")
}

func main() {
	flag.Parse()

	logger, err := logging.NewLogger(logLevel)
	if err != nil {
		log.Fatalf("Error creating logger: %v", err)
	}
	defer logger.Sync()

	stopCh := signals.SetupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		logger.Fatalf("Error building kubeconfig: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Fatalf("Error building kubernetes clientset: %v", err)
	}

	sharedClient, err := sharedclientset.NewForConfig(cfg)
	if err != nil {
		logger.Fatalf("Error building shared clientset: %v", err)
	}

	flaggerClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		logger.Fatalf("Error building example clientset: %s", err.Error())
	}

	flaggerInformerFactory := informers.NewSharedInformerFactory(flaggerClient, time.Second*30)
	canaryInformer := flaggerInformerFactory.Flagger().V1alpha1().Canaries()

	logger.Infof("Starting flagger version %s revision %s", version.VERSION, version.REVISION)

	ver, err := kubeClient.Discovery().ServerVersion()
	if err != nil {
		logger.Fatalf("Error calling Kubernetes API: %v", err)
	}

	logger.Infof("Connected to Kubernetes API %s", ver)

	ok, err := controller.CheckMetricsServer(metricsServer)
	if ok {
		logger.Infof("Connected to metrics server %s", metricsServer)
	} else {
		logger.Errorf("Metrics server %s unreachable %v", metricsServer, err)
	}

	// start HTTP server
	go server.ListenAndServe(port, 3*time.Second, logger, stopCh)

	c := controller.NewController(
		kubeClient,
		sharedClient,
		flaggerClient,
		canaryInformer,
		controlLoopInterval,
		metricsServer,
		logger,
	)

	flaggerInformerFactory.Start(stopCh)

	logger.Info("Waiting for informer caches to sync")
	for _, synced := range []cache.InformerSynced{
		canaryInformer.Informer().HasSynced,
	} {
		if ok := cache.WaitForCacheSync(stopCh, synced); !ok {
			logger.Fatalf("Failed to wait for cache sync")
		}
	}

	// start controller
	go func(ctrl *controller.Controller) {
		if err := ctrl.Run(2, stopCh); err != nil {
			logger.Fatalf("Error running controller: %v", err)
		}
	}(c)

	<-stopCh
}
