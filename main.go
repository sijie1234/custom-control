package main

import (
	"flag"
	"time"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	clientset "gitweb/pkg/client/clientset/versioned"
	"k8s.io/client-go/informers"
	pwinformers "gitweb/pkg/client/informers/externalversions"
	kubeinformers "k8s.io/client-go/informers"
	"gitweb/pkg"
	"os/signal"
	"os"
	"syscall"
)
var (
	masterURL  string
	kubeconfig string
)

func main() {
	flag.Parse()
	stopCh := SetupSignalHandler()
	kubeConfigPath := "/Users/sijie/.kube/config"
	cfg,err := clientcmd.BuildConfigFromFlags(masterURL,kubeConfigPath)
	if err != nil {
		log.Fatal("Error building kubeconfig: %s", err.Error())
	}
	kubeclient := kubernetes.NewForConfigOrDie(cfg)

	gwclient,err := clientset.NewForConfig(cfg)
	if err != nil {
		log.Fatal("Error building example clientset: %s", err.Error())
	}
	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeclient, 0, informers.WithTweakListOptions(func(opt *metav1.ListOptions) {
	}))
	informerFactory = kubeinformers.NewSharedInformerFactory(kubeclient, time.Second*30)
	podInformer := informerFactory.Core().V1().Pods()
	gwInformerFactory := pwinformers.NewSharedInformerFactory(gwclient,time.Second*30)
	controller := pkg.NewGitWebController(kubeclient,gwInformerFactory.Samplecrd().V1().Foos(),podInformer,gwclient)
	go gwInformerFactory.Start(stopCh)
	go informerFactory.Start(stopCh)
	controller.Run(stopCh)
}

var onlyOneSignalHandler = make(chan struct{})

var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
func SetupSignalHandler() (stopCh <-chan struct{}) {
	close(onlyOneSignalHandler) // panics when called twice

	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		<-c
		close(stop)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()

	return stop
}