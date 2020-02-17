package kuberneteswatcher

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// WatchPvcData Holds the data to  be sent to the pvc Watch channel
type WatchPvcData struct {
	LogEntry     log.Entry
	ListOptions  metaV1.ListOptions
	RegistryData RegistryData
	Namespace    string
	Pod          string
	Ctx          context.Context
}

// PvcManager manages the Pvc
type PvcManager struct {
	eventManager  *EventsManager
	client        kubernetes.Interface
	Watch         chan WatchPvcData
	pvcsFirstInit map[string]bool
	mutex         *sync.RWMutex
}

// NewPvcManager creates a new pvc manager objects
func NewPvcManager(kubernetesClientset kubernetes.Interface, eventManager *EventsManager) *PvcManager {
	return &PvcManager{
		client:        kubernetesClientset,
		eventManager:  eventManager,
		pvcsFirstInit: map[string]bool{},
		mutex:         &sync.RWMutex{},
		Watch:         make(chan WatchPvcData),
	}
}

// Serve will start listening on Pvc requests
func (pm *PvcManager) Serve(ctx context.Context, wg *sync.WaitGroup) {
	go func() {
		for {
			select {
			case data := <-pm.Watch:
				pm.watch(data)
			case <-ctx.Done():
				log.Warn("Pvc Manager has been shut down")
				wg.Done()
				return
			}
		}
	}()
}

// storePodFirstInit will set if some pvc appears for the first time true == first time
func (pm *PvcManager) storePvcFirstInit(key string, val bool) {
	pm.mutex.Lock()
	pm.pvcsFirstInit[key] = val
	pm.mutex.Unlock()
}

// loadPvcFirstInit  will return true if Pvc exists or false otherwise
func (pm *PvcManager) loadPvcFirstInit(key string) bool {
	pm.mutex.RLock()
	exist := pm.pvcsFirstInit[key]
	pm.mutex.RUnlock()
	return exist
}

// watch will start watch on pvcs changes
func (pm *PvcManager) watch(watchPvcData WatchPvcData) {
	go func() {

		watchPvcData.LogEntry.Info("Started Pvc Watcher")
		watchPvcData.LogEntry.WithField("name", watchPvcData.RegistryData.GetName())
		watchPvcData.LogEntry.WithField("list_options", watchPvcData.ListOptions).Debug("Started watching Pvcs with the following list options")

		watcher, err := pm.client.CoreV1().PersistentVolumeClaims(watchPvcData.Namespace).Watch(watchPvcData.ListOptions)
		if err != nil {
			watchPvcData.LogEntry.WithError(err).WithField("list_options", watchPvcData.ListOptions.String()).Error("An error occurred while trying to start the Pvc watcher")
			return
		}
		for {
			select {
			case event, watch := <-watcher.ResultChan():
				if !watch {
					watchPvcData.LogEntry.WithField("object", event.Object).Warn("Pvc watcher was stopped. Channel was closed")
					return
				}

				pvc, ok := event.Object.(*coreV1.PersistentVolumeClaim)
				if !ok {
					watchPvcData.LogEntry.Warn("Failed to parse Pvc watch data")
					continue
				}

				lg := watchPvcData.LogEntry.WithFields(log.Fields{
					"pvc": pvc.Name,
					"pod": watchPvcData.Pod,
				})

				//If it is the first time that we got the pvc, we are starting a watch on pvc events & send the pvc to the registry
				if found := pm.loadPvcFirstInit(pvc.Name); !found {
					lg.Debug("New pvc was found")
					pm.storePvcFirstInit(pvc.Name, true)

					eventListOptions := metaV1.ListOptions{FieldSelector: labels.SelectorFromSet(map[string]string{
						"involvedObject.name": pvc.GetName(),
						"involvedObject.kind": "PersistentVolumeClaim"}).String()}

					go pm.watchEvents(watchPvcData.Ctx, *lg, watchPvcData.RegistryData, eventListOptions, pvc.Namespace, watchPvcData.Pod, pvc.GetName())
				}

			case <-watchPvcData.Ctx.Done():
				watchPvcData.LogEntry.Info("Pvc watcher was stopped. Got ctx done signal")
				watcher.Stop()
				return
			}
		}
	}()

}

func (pm *PvcManager) watchEvents(ctx context.Context, lg log.Entry, registryData RegistryData, listOptions metaV1.ListOptions, namespace, podName, pvcName string) {

	lg.Info("Started watching on Pvc events")

	watchPvcData := WatchEvents{
		ListOptions: listOptions,
		Namespace:   namespace,
		Ctx:         ctx,
		LogEntry:    lg,
	}

	eventChan := pm.eventManager.Watch(watchPvcData)
	go func() {

		for {
			select {
			case event := <-eventChan:
				registryData.UpdatePodEvents(podName, pvcName, event)
			case <-ctx.Done():
				lg.Info("Stopped watching on Pvc events")
				return
			}

		}
	}()

}
