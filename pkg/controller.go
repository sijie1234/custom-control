package pkg

import (
	"context"
	"fmt"
	gitv1 "gitweb/pkg/apis/samplecrd/v1"
	clientset "gitweb/pkg/client/clientset/versioned"
	informer "gitweb/pkg/client/informers/externalversions/samplecrd/v1"
	listers "gitweb/pkg/client/listers/samplecrd/v1"
	"log"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const controllerAgentName = "gitweb-controller"

var (
	DefaultPendingSecodns int32 = 120
	Pending = make(map[string]int32)
)
type Controller struct {
	kubeclientset    kubernetes.Interface
	gitwebclientset  clientset.Interface
	gitwebLister     listers.FooLister
	gitwebListSynced cache.InformerSynced
	podLister        corelister.PodLister
	podListSynced    cache.InformerSynced
	eventRecorder    record.EventRecorder
	pgQueue          workqueue.RateLimitingInterface
}

func NewGitWebController(client kubernetes.Interface,
	gwInformer informer.FooInformer, podinformer coreinformer.PodInformer, gwclient clientset.Interface) *Controller {
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&corev1.EventSinkImpl{Interface: client.CoreV1().Events(v1.NamespaceAll)})
	ctr := &Controller{eventRecorder: broadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: "gitweb"}),
		pgQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "gitweb")}
	gwInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: ctr.addGitWeb,
	})

	podinformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: ctr.addGitWebPod,
	})
	ctr.gitwebLister = gwInformer.Lister()
	ctr.podLister = podinformer.Lister()
	ctr.gitwebListSynced = gwInformer.Informer().HasSynced
	ctr.podListSynced = podinformer.Informer().HasSynced
	ctr.gitwebclientset = gwclient
	ctr.kubeclientset = client
	return ctr
}

func (ctr *Controller) Run(stop <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer ctr.pgQueue.ShutDown()
	log.Println("Starting gitweb control loop")
	if ok := cache.WaitForCacheSync(stop, ctr.gitwebListSynced,ctr.podListSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	log.Println("Starting workers")
	for i := 0; i < 2; i++ {
		go wait.Until(ctr.runWorker, time.Second, stop)
	}
	log.Println("Started workers")
	<-stop
	log.Println("Shutting down workers")
	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.pgQueue.Get()
	if shutdown {
		return false
	}
	err := func(obj interface{}) error {
		defer c.pgQueue.Done(obj)
		var key string
		var ok bool
		if key, ok = obj.(string); !ok {
			c.pgQueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		c.pgQueue.Forget(obj)
		log.Println("Successfully synced '%s'", key)
		return nil
	}(obj)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	return false
}

func (c *Controller) syncHandler(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}
	gitweb, err := c.gitwebLister.Foos(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Println("GitWeb: %s/%s does not exist in local cache, will delete it from Neutron ...", namespace, name)
			return nil
		}
		runtime.HandleError(fmt.Errorf("failed to list gitweb by: %s/%s", namespace, name))
		return err
	}
	gw := gitweb.DeepCopy()
	selector := labels.Set(map[string]string{"gitwebsite":gw.Name}).AsSelector()
	fmt.Println(selector)
	pods, err := c.podLister.Pods(gw.Namespace).List(selector)
	alive, noalive, failed,runnings,faileds,pendings := AcculatePod(pods)
	if noalive > 5 {
		log.Println("非running 太多，待运行")
		return fmt.Errorf("非running 太多，待运行")
	}
	if gw.Status.AvailableReplicas < *gw.Spec.Replicas || alive < *gw.Spec.Replicas {
		_, err := c.kubeclientset.CoreV1().Pods(gitweb.Namespace).Create(context.Background(), newPod1(gw), metav1.CreateOptions{})
		if err != nil {
			log.Println("GitWeb Create pod failed,reason:%v", err)
			return err
		}
		gw.Status.AvailableReplicas  = alive + 1
		_, err = c.gitwebclientset.SamplecrdV1().Foos(gitweb.Namespace).Update(context.Background(), gw, metav1.UpdateOptions{})
		if err != nil {
			log.Panicln("update ",gw.Name," error,error:",err.Error())
			return nil
		}
	}
	if failed != 0 {
		for  pod,_ := range faileds {
			err = c.kubeclientset.CoreV1().Pods(gitweb.Namespace).Delete(context.Background(), pod, metav1.DeleteOptions{})
			if err != nil {
				return err
			}
			delete(faileds,pod)
		}
	}
	if gw.Status.AvailableReplicas > *gw.Spec.Replicas {
		num := gw.Status.AvailableReplicas - *gw.Spec.Replicas
		temp := make(map[int32]string)
		var tempInt int32 = 1
		for pod,_ := range runnings {
			temp[tempInt] = pod
			tempInt ++
		}
		var i int32
		for i =1; i <= num; i ++ {
			err = c.kubeclientset.CoreV1().Pods(gitweb.Namespace).Delete(context.Background(), temp[i], metav1.DeleteOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func newPod(webgit *gitv1.Foo) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webgit.Name + "-" + fmt.Sprintf("%d", time.Now().Unix()),
			Namespace: webgit.Namespace,
			Labels:    map[string]string{"gitwebsite": webgit.Name},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(webgit, gitv1.SchemeGroupVersion.WithKind("Foo")),
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "html",
							MountPath: "/usr/share/nginx/html",
							ReadOnly:  true,
						},
					},
					Ports: []v1.ContainerPort{
						{
							ContainerPort: 80,
							Protocol:      "TCP",
						},
					},
				},
				/*
					{
						// git sync container for fetching code
						Name:  "git-sync",
						Image: "openweb/git-sync",
						Env: []v1.EnvVar{
							{
								Name:  "GIT_SYNC_REPO",
								Value: webgit.Spec.Url,
							},
							{
								Name:  "GIT_SYNC_DEST",
								Value: "/gitrepo",
							},
							{
								Name:  "GIT_SYNC_BRANCH",
								Value: "master",
							},
							{
								Name:  "GIT_SYNC_REV",
								Value: "FETCH_HEAD",
							},
							{
								Name:  "GIT_SYNC_WAIT",
								Value: "3600",
							},
						},
						VolumeMounts: []v1.VolumeMount{
							{
								Name:      "html",
								MountPath: "/gitrepo",
							},
						},
					},
				*/
			},
			Volumes: []v1.Volume{
				{
					"html",
					v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: "/data/test",
						},
					},
				},
			},
		},
	}
}

func newPod1(webgit *gitv1.Foo) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webgit.Name + "-" + fmt.Sprintf("%d", time.Now().Unix()),
			Namespace: webgit.Namespace,
			Labels:    map[string]string{"gitwebsite": webgit.Name},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(webgit, gitv1.SchemeGroupVersion.WithKind("Foo")),
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
					Ports: []v1.ContainerPort{
						{
							ContainerPort: 80,
							Protocol:      "TCP",
						},
					},
				},
			},
		},
	}
}

func AcculatePod(pods []*v1.Pod) (int32, int32, int32,map[string]int32,map[string]int32,map[string]int32) {
	var alive int32
	var notAlive int32
	var failed int32
	running := make(map[string]int32)
	faileds := make(map[string]int32)
	pendings := make(map[string]int32)


	for _, pod := range pods {
		if pod.Status.Phase == v1.PodRunning || pod.Status.Phase == v1.PodPending{
			alive++
			running[pod.Name] = 0
		} else if pod.Status.Phase == v1.PodPending{
			pendings[pod.Name]  = 0
		}else{
			failed ++
			faileds[pod.Name]  = 1
		}
	}
	return alive, notAlive, failed,running,faileds,pendings
}

func (c *Controller) addGitWeb(obj interface{}) {
	if _, ok := obj.(*gitv1.Foo); !ok {
		return
	}
	gitweb := obj.(*gitv1.Foo)
	gw, err := c.gitwebLister.Foos(gitweb.Namespace).Get(gitweb.Name)
	if err != nil {
		log.Println(err.Error())
		return
	}
	log.Println("gitweb change into git queue")
	c.gitwebQueue(gw)
}

func (c *Controller) addGitWebPod(obj interface{}) {
	if _, ok := obj.(*v1.Pod); !ok {
		return
	}
	pd := obj.(*v1.Pod)
	if pd.DeletionTimestamp != nil {
		return
	}
	if _, ok := pd.Labels["gitwebsite"]; !ok {
		return
	}
	gw, err := c.gitwebLister.Foos(pd.Namespace).Get(pd.Labels["gitwebsite"])
	if errors.IsNotFound(err) {
		return
	}
	log.Println("pod change into git queue")
	c.gitwebQueue(gw)
}

func (c *Controller) gitwebQueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		log.Println(err)
		return
	}
	c.pgQueue.Add(key)
}
