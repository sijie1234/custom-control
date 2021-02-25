package main

import (
	"flag"
	"time"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/staging/src/k8s.io/sample-controller/pkg/signals"

	"log"
	clientset "gitweb/pkg/client/clientset/versioned"
	"k8s.io/client-go/informers"
	pwinformers "gitweb/pkg/client/informers/externalversions"
)
var (
	masterURL  string
	kubeconfig string
)

func main() {
	flag.Parse()
	stopCh := signals.SetupSignalHandler()
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
	podInformer := informerFactory.Core().V1().Pods()
	gwInformerFactory := pwinformers.NewSharedInformerFactory(gwclient,time.Second*30)
	controller := NewGitWebController(kubeclient,gwInformerFactory.Samplecrd().V1().Foos(),podInformer,gwclient)
	go gwInformerFactory.Start(stopCh)
	controller.Run(stopCh)
}


